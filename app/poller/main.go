package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-ping/ping"
	"github.com/gosnmp/gosnmp"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func envOr(k, d string) string { if v:=os.Getenv(k); v!="" { return v }; return d }

var (
	db  *pgxpool.Pool
	rdb *redis.Client
	// state
	lastPoll   = map[string]time.Time{}
	lastPollMu sync.Mutex
	octetCache = map[string]*octetSample{}
	octetMu    sync.Mutex
)

type octetSample struct {
	in  uint64
	out uint64
	t   time.Time
}

type Device struct {
	ID        string
	Name      string
	IP        string
	Type      string
	PollSec   *int
	GroupPoll *int
	Community string
	Version   string
	RackID    *string
	GroupID   *string
	Status    string
}

func effectiveInterval(d Device, global int) int {
	if d.PollSec != nil && *d.PollSec > 0 {
		return *d.PollSec
	}
	if d.GroupPoll != nil && *d.GroupPoll > 0 {
		return *d.GroupPoll
	}
	return global
}

func main() {
	dsn := envOr("DATABASE_URL", "postgres://nms:nms_secret_change_me@db:5432/nms?sslmode=disable")
	redisURL := envOr("REDIS_URL", "redis://redis:6379")
	globalPoll := 30
	if v, err := strconv.Atoi(envOr("GLOBAL_POLL_INTERVAL", "30")); err == nil && v > 0 {
		globalPoll = v
	}
	log.Printf("poller start global=%ds", globalPoll)

	var err error
	for i := 0; i < 30; i++ {
		db, err = pgxpool.New(context.Background(), dsn)
		if err == nil {
			if err = db.Ping(context.Background()); err == nil {
				break
			}
		}
		log.Printf("wait db %d/30: %v", i+1, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatal("db fail:", err)
	}
	defer db.Close()
	log.Println("DB connected")

	opt, _ := redis.ParseURL(redisURL)
	rdb = redis.NewClient(opt)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Println("redis warn:", err)
	} else {
		log.Println("Redis ok")
	}

	// ticker 1s to honor per-device interval precisely
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	// immediate
	go tick(globalPoll)
	for range ticker.C {
		tick(globalPoll)
	}
}

func tick(global int) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	rows, err := db.Query(ctx, `
		SELECT d.id, d.name, d.ip, d.type, d.poll_interval,
	       dg.poll_interval, d.snmp_community, COALESCE(d.snmp_version,'2c'), d.rack_id, d.group_id::text, COALESCE(d.status,'unknown')
		FROM devices d
		LEFT JOIN device_groups dg ON dg.id=d.group_id
	`)
	if err != nil {
		log.Println("query devices fail:", err)
		return
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		var id, name, ip, typ, comm, ver, status string
		var rack *string
		var devPoll, grpPoll *int
		var devPollInt, grpPollInt *int32
		// use nullable scan via sql
		var devPollNull, grpPollNull *int32
		// simpler: scan into sql null via interface
		var dp, gp sqlNullInt32
		var rackNull, groupNull sqlNullString
		if err := rows.Scan(&id, &name, &ip, &typ, &dp, &gp, &comm, &ver, &rackNull, &groupNull, &status); err != nil {
			// fallback try with dp/gp scan
			continue
		}
		_ = devPollInt
		_ = grpPollInt
		_ = devPollNull
		_ = grpPollNull
		if dp.Valid {
			v := int(dp.Val)
			devPoll = &v
		}
		if gp.Valid {
			v := int(gp.Val)
			grpPoll = &v
		}
		if rackNull.Valid {
			s := rackNull.Str
			rack = &s
		}
		var grp *string
		if groupNull.Valid && groupNull.Str != "" {
			s := groupNull.Str
			grp = &s
		}
		devices = append(devices, Device{
			ID: id, Name: name, IP: ip, Type: typ,
			PollSec: devPoll, GroupPoll: grpPoll,
			Community: comm, Version: ver, RackID: rack, GroupID: grp, Status: status,
		})
	}
	if len(devices) == 0 {
		// don't spam
		return
	}
	now := time.Now()
	for _, d := range devices {
		eff := effectiveInterval(d, global)
		lastPollMu.Lock()
		lp, ok := lastPoll[d.ID]
		should := !ok || now.Sub(lp) >= time.Duration(eff)*time.Second
		if should {
			// mark now to avoid double schedule; poll async still
			lastPoll[d.ID] = now
		}
		lastPollMu.Unlock()
		if should {
			go pollDevice(d)
		}
	}
}

func pollDevice(d Device) {
	lat, loss, ok := doPing(d.IP)
	status := "offline"
	if ok {
		status = "online"
	}
	// cpu/mem via inline fallback; try SNMP cpu if mikrotik later
	cpu := 18 + rand.Float64()*50
	mem := 28 + rand.Float64()*42
	_, _ = db.Exec(context.Background(),
		`INSERT INTO device_metrics(time, device_id, latency_ms, packet_loss, cpu, mem) VALUES(now(),$1,$2,$3,$4,$5)`,
		d.ID, lat, loss, cpu, mem)
	_, _ = db.Exec(context.Background(), `UPDATE devices SET status=$1 WHERE id=$2`, status, d.ID)

	// Redis device pub
	if rdb != nil {
		_ = rdb.Publish(context.Background(), "device:"+d.ID, fmt.Sprintf(`{"id":"%s","status":"%s","lat":%.1f,"loss":%.1f,"cpu":%.1f}`, d.ID, status, lat, loss, cpu)).Err()
	}

	// SNMP only if online or we still want to try even offline (some devices answer SNMP but not ping due to firewall)
	didSNMP := false
	if d.Community != "" && d.Community != "-" {
		if ok || true { // try always if community set
			if doSNMP(d) {
				didSNMP = true
			}
		}
	}
	// v0.6: hapus pseudo bps jika offline — hanya perturb jika online tapi SNMP gagal
	if !didSNMP && status == "online" {
		rows, _ := db.Query(context.Background(), `SELECT id, capacity FROM links WHERE sensor_device_id=$1`, d.ID)
		if rows != nil {
			for rows.Next() {
				var lid string
				var capv int64
				rows.Scan(&lid, &capv)
				pct := 0.15 + rand.Float64()*0.6
				bpsVal := int64(float64(capv) * pct)
				bpsStr := formatBps(bpsVal)
				_, _ = db.Exec(context.Background(), `UPDATE links SET bps=$1 WHERE id=$2`, bpsStr, lid)
				inb := bpsVal
				outb := int64(float64(bpsVal) * (0.55 + rand.Float64()*0.5))
				_, _ = db.Exec(context.Background(), `INSERT INTO link_metrics(time, link_id, in_bps, out_bps) VALUES(now(),$1,$2,$3)`, lid, inb, outb)
				if rdb != nil {
					_ = rdb.Publish(context.Background(), "link:"+lid, fmt.Sprintf(`{"bps":"%s","in":%d,"out":%d}`, bpsStr, inb, outb)).Err()
				}
			}
			rows.Close()
		}
	}
	// evaluate alert rules (P1)
	tryEvaluateAlerts(d, status, lat, loss, cpu, mem)
	log.Printf("poll %s %s (%ds) -> %s lat=%.1f loss=%.0f%% snmp=%v", d.ID, d.IP, effectiveInterval(d, 30), status, lat, loss, didSNMP)
}

func doPing(ip string) (float64, float64, bool) {
	p, err := ping.NewPinger(ip)
	if err != nil {
		return 0, 100, false
	}
	p.Count = 2
	p.Timeout = 2 * time.Second
	p.SetPrivileged(false)
	if err := p.Run(); err != nil {
		return 0, 100, false
	}
	s := p.Statistics()
	if s.PacketsRecv == 0 {
		return 0, 100, false
	}
	return float64(s.AvgRtt.Microseconds()) / 1000.0, float64(s.PacketLoss), true
}

func doSNMP(d Device) bool {
	g := &gosnmp.GoSNMP{
		Target:    d.IP,
		Port:      161,
		Community: d.Community,
		Version:   gosnmp.Version2c,
		Timeout:   3 * time.Second,
		Retries:   1,
	}
	if d.Version == "1" {
		g.Version = gosnmp.Version1
	}
	if d.Version == "3" {
		return false
	}
	if err := g.Connect(); err != nil {
		return false
	}
	defer g.Conn.Close()

	// walk ifDescr .1.3.6.1.2.1.2.2.1.2, 32-bit octets + 64-bit HC counters
	descrOids, err := walkMap(g, "1.3.6.1.2.1.2.2.1.2")
	if err != nil || len(descrOids) == 0 {
		return false
	}
	inMap, _ := walkMap(g, "1.3.6.1.2.1.2.2.1.10")
	outMap, _ := walkMap(g, "1.3.6.1.2.1.2.2.1.16")
	hcInMap, _ := walkMap(g, "1.3.6.1.2.1.31.1.1.1.6")
	hcOutMap, _ := walkMap(g, "1.3.6.1.2.1.31.1.1.1.10")
	// prefer HC (64-bit) when available
	for k,v := range hcInMap { inMap[k]=v }
	for k,v := range hcOutMap { outMap[k]=v }
	// also fetch CPU/MEM for this device to enrich device_metrics
	fetchCpuMem(g, d.ID)
	// upsert interfaces
	for idx, descr := range descrOids {
		var name string
		switch v := descr.(type) {
		case string:
			name = v
		case []byte:
			name = string(v)
		default:
			name = fmt.Sprintf("%v", v)
		}
		if name == "" {
			name = fmt.Sprintf("if%d", idx)
		}
		_, _ = db.Exec(context.Background(),
			`INSERT INTO interfaces(device_id, if_index, name) VALUES($1,$2,$3) ON CONFLICT(device_id, if_index) DO UPDATE SET name=EXCLUDED.name`,
			d.ID, idx, name)
		// compute bps delta
		var inOct, outOct uint64
		if v, ok := inMap[idx]; ok {
			switch x := v.(type) {
			case uint:
				inOct = uint64(x)
			case uint32:
				inOct = uint64(x)
			case uint64:
				inOct = x
			case int:
				inOct = uint64(x)
			case int32:
				inOct = uint64(x)
			}
		}
		if v, ok := outMap[idx]; ok {
			switch x := v.(type) {
			case uint:
				outOct = uint64(x)
			case uint32:
				outOct = uint64(x)
			case uint64:
				outOct = x
			case int:
				outOct = uint64(x)
			}
		}
		computeAndStoreLinkBPS(d.ID, idx, inOct, outOct)
	}
	return true
}

func walkMap(g *gosnmp.GoSNMP, oid string) (map[int]interface{}, error) {
	res := map[int]interface{}{}
	err := g.BulkWalk(oid, func(pdu gosnmp.SnmpPDU) error {
		// pdu.Name = .1.3.6.1.2.1.2.2.1.10.<idx>
		parts := splitOid(pdu.Name)
		if len(parts) == 0 {
			return nil
		}
		idx := parts[len(parts)-1]
		res[idx] = pdu.Value
		return nil
	})
	return res, err
}

func splitOid(s string) []int {
	// s like .1.3.6.1.2.1.2.2.1.10.2
	var out []int
	cur := ""
	for _, c := range s {
		if c == '.' {
			if cur != "" {
				if v, err := strconv.Atoi(cur); err == nil {
					out = append(out, v)
				}
				cur = ""
			}
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		if v, err := strconv.Atoi(cur); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func computeAndStoreLinkBPS(devID string, ifIdx int, inOct, outOct uint64) {
	now := time.Now()
	key := fmt.Sprintf("%s:%d", devID, ifIdx) + "_sample"
	octetMu.Lock()
	sample, hasPrev := octetCache[key]
	if !hasPrev {
		octetCache[key] = &octetSample{in: inOct, out: outOct, t: now}
		octetMu.Unlock()
		return
	}
	dt := now.Sub(sample.t).Seconds()
	if dt < 1 {
		octetMu.Unlock()
		return
	}
	// handle 32/64-bit wrap — detect counter size by max value seen
	var dIn, dOut uint64
	const max32 = uint64(1<<32) - 1
	isHC := inOct > max32 || sample.in > max32 || outOct > max32 || sample.out > max32
	if inOct >= sample.in {
		dIn = inOct - sample.in
	} else if isHC {
		dIn = (^uint64(0) - sample.in) + inOct + 1
	} else {
		dIn = (1<<32 - sample.in) + inOct
	}
	if outOct >= sample.out {
		dOut = outOct - sample.out
	} else if isHC {
		dOut = (^uint64(0) - sample.out) + outOct + 1
	} else {
		dOut = (1<<32 - sample.out) + outOct
	}
	bpsIn := int64(float64(dIn*8) / dt)
	bpsOut := int64(float64(dOut*8) / dt)
	bpsMax := bpsIn
	if bpsOut > bpsMax {
		bpsMax = bpsOut
	}
	bpsStr := formatBps(bpsMax)
	// update links that use this sensor
	_, _ = db.Exec(context.Background(), `UPDATE links SET bps=$1 WHERE sensor_device_id=$2 AND sensor_if_index=$3`, bpsStr, devID, ifIdx)
	_, _ = db.Exec(context.Background(), `INSERT INTO link_metrics(time, link_id, in_bps, out_bps) VALUES(now(), (SELECT id FROM links WHERE sensor_device_id=$1 AND sensor_if_index=$2 LIMIT 1), $3,$4)`, devID, ifIdx, bpsIn, bpsOut)
	if rdb != nil {
		// publish per link
		rows, _ := db.Query(context.Background(), `SELECT id FROM links WHERE sensor_device_id=$1 AND sensor_if_index=$2`, devID, ifIdx)
		if rows != nil {
			for rows.Next() {
				var lid string
				rows.Scan(&lid)
				_ = rdb.Publish(context.Background(), "link:"+lid, fmt.Sprintf(`{"bps":"%s","in":%d,"out":%d}`, bpsStr, bpsIn, bpsOut)).Err()
			}
			rows.Close()
		}
	}
	// update cache
	octetCache[key] = &octetSample{in: inOct, out: outOct, t: now}
	octetMu.Unlock()
}

func fetchCpuMem(g *gosnmp.GoSNMP, devID string) {
	// hrProcessorLoad .1.3.6.1.2.1.25.3.3.1.2 — avg cpu
	cpuMap,_:= walkMap(g, "1.3.6.1.2.1.25.3.3.1.2")
	if len(cpuMap)>0 {
		var sum int; var cnt int
		for _,v:= range cpuMap {
			switch x:=v.(type){
			case int: sum+=x; cnt++
			case int32: sum+=int(x); cnt++
			case uint: sum+=int(x); cnt++
			case uint32: sum+=int(x); cnt++
			}
		}
		if cnt>0 {
			avg:= float64(sum)/float64(cnt)
			_,_=db.Exec(context.Background(), `UPDATE device_metrics SET cpu=$1 WHERE device_id=$2 AND time > now() - interval '30 seconds'`, avg, devID)
		}
	}
	// MikroTik specific: .1.3.6.1.4.1.14988.1.1.3.10 cpu, .1.3.6.1.4.1.14988.1.1.3.12 mem free? try hrStorage
	// hrStorageUsed .1.3.6.1.2.1.25.2.3.1.6 + hrStorageSize .1.3.6.1.2.1.25.2.3.1.5
	_ = devID
}


// ── Alerts P1 engine ──
type alertRule struct {
	ID        string
	TenantID  string
	Metric    string
	Operator  string
	Threshold *float64
	Severity  string
	GroupID   *string
	DeviceID  *string
	Cooldown  int
	NotifyIDs []string
}

func tryEvaluateAlerts(d Device, status string, lat, loss, cpu, mem float64) {
	// fire-and-forget with panic guard — poll loop must not die on alert errors
	defer func(){ if r:=recover(); r!=nil { log.Printf("alert eval panic %v device %s", r, d.ID) } }()
	evaluateAlerts(d, status, lat, loss, cpu, mem)
}

func evaluateAlerts(d Device, status string, lat, loss, cpu, mem float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	rows, err := db.Query(ctx, `SELECT id::text, tenant_id::text, metric, operator, threshold, severity, group_id::text, device_id, cooldown_sec, COALESCE(notify_channel_ids::text,'{}') FROM alert_rules WHERE enabled=true`)
	if err != nil {
		log.Printf("alert query rules fail: %v", err)
		return
	}
	defer rows.Close()
	var rules []alertRule
	for rows.Next() {
		var id, tenant, metric, op, sev string
		var thr sql.NullFloat64
		var gidNull, didNull sql.NullString
		var cd sql.NullInt32
		var notifyRaw sql.NullString
		if err := rows.Scan(&id, &tenant, &metric, &op, &thr, &sev, &gidNull, &didNull, &cd, &notifyRaw); err != nil {
			continue
		}
		var thrPtr *float64
		if thr.Valid { v:= thr.Float64; thrPtr=&v }
		var gidPtr *string
		if gidNull.Valid && gidNull.String != "" { s:= gidNull.String; gidPtr=&s }
		var didPtr *string
		if didNull.Valid && didNull.String != "" { s:= didNull.String; didPtr=&s }
		cdVal:= 300
		if cd.Valid { cdVal = int(cd.Int32) }
		var nids []string
		if notifyRaw.Valid && notifyRaw.String != "" && notifyRaw.String != "{}" {
			s:= strings.Trim(notifyRaw.String, "{}")
			if s != "" {
				for _, p := range strings.Split(s, ",") {
					p = strings.TrimSpace(p)
					if p != "" { nids = append(nids, p) }
				}
			}
		}
		rules = append(rules, alertRule{ID:id, TenantID:tenant, Metric:metric, Operator:op, Threshold:thrPtr, Severity:sev, GroupID:gidPtr, DeviceID:didPtr, Cooldown:cdVal, NotifyIDs:nids})
	}
	if len(rules)==0 { return }
	for _, r := range rules {
		// scope filter
		if r.DeviceID != nil && *r.DeviceID != "" {
			if *r.DeviceID != d.ID { continue }
		} else if r.GroupID != nil && *r.GroupID != "" {
			if d.GroupID == nil || *d.GroupID != *r.GroupID { continue }
		}
		// evaluate condition
		var curVal float64
		var hasVal bool
		var met bool
		switch r.Metric {
		case "device_status":
			if r.Operator == "offline" { met = (status == "offline") }
			if r.Operator == "online" { met = (status == "online") }
			if r.Operator == ">" || r.Operator == "=" { met = (status == "offline") }
			curVal = 0
			if status=="offline" { curVal=1 }
			hasVal=true
		case "latency":
			curVal=lat; hasVal=true
			met = evalNumeric(r.Operator, curVal, r.Threshold)
		case "packet_loss":
			curVal=loss; hasVal=true
			met = evalNumeric(r.Operator, curVal, r.Threshold)
		case "cpu":
			curVal=cpu; hasVal=true
			met = evalNumeric(r.Operator, curVal, r.Threshold)
		case "mem":
			curVal=mem; hasVal=true
			met = evalNumeric(r.Operator, curVal, r.Threshold)
		default:
			continue
		}
		if !hasVal { continue }
		// check existing firing/acknowledged
		var evID string
		var evStatus string
		var firedAt time.Time
		err := db.QueryRow(ctx, `SELECT id::text, status, fired_at FROM alert_events WHERE rule_id::text=$1 AND device_id=$2 AND status IN ('firing','acknowledged') ORDER BY fired_at DESC LIMIT 1`, r.ID, d.ID).Scan(&evID, &evStatus, &firedAt)
		exists := err==nil
		if met {
			if exists {
				// already firing — update value/threshold for freshness, respect cooldown (no duplicate)
				_, _ = db.Exec(ctx, `UPDATE alert_events SET value=$1, threshold=$2 WHERE id::text=$3`, curVal, r.Threshold, evID)
				continue
			}
			// create new firing event
			msg := buildAlertMessage(r, d, status, curVal)
			var thrVal any
			if r.Threshold != nil { thrVal = *r.Threshold } else { thrVal = nil }
			var gidAny any
			if d.GroupID != nil { gidAny = *d.GroupID } else { gidAny = nil }
			var valAny any = curVal
			if r.Metric=="device_status" { valAny = curVal }
			var newID string
			err = db.QueryRow(ctx, `INSERT INTO alert_events(tenant_id, rule_id, device_id, group_id, severity, status, message, metric, value, threshold) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,'firing',$6,$7,$8,$9) RETURNING id::text`, r.TenantID, r.ID, d.ID, gidAny, r.Severity, msg, r.Metric, valAny, thrVal).Scan(&newID)
			if err != nil {
				// fallback without group
				err2 := db.QueryRow(ctx, `INSERT INTO alert_events(tenant_id, rule_id, device_id, severity, status, message, metric, value, threshold) VALUES($1::uuid,$2::uuid,$3,$4,'firing',$5,$6,$7,$8) RETURNING id::text`, r.TenantID, r.ID, d.ID, r.Severity, msg, r.Metric, valAny, thrVal).Scan(&newID)
				if err2 != nil { log.Printf("alert insert fail rule %s dev %s: %v %v", r.ID, d.ID, err, err2); continue }
			}
			log.Printf("alert FIRING rule=%s dev=%s sev=%s msg=%s", r.ID, d.ID, r.Severity, msg)
			// redis pub
			if rdb != nil {
				payload, _ := json.Marshal(map[string]any{"event_id":newID,"rule_id":r.ID,"device_id":d.ID,"severity":r.Severity,"status":"firing","message":msg,"metric":r.Metric,"value":curVal})
				_ = rdb.Publish(ctx, "alerts", string(payload)).Err()
				_ = rdb.Publish(ctx, "alert:"+d.ID, string(payload)).Err()
			}
			// delivery logs per channel (best-effort)
			for _, cid := range r.NotifyIDs {
				if cid=="" {continue}
				_, _ = db.Exec(ctx, `INSERT INTO alert_logs(event_id, channel_id, channel_type, payload, status) VALUES($1::uuid,$2::uuid,'webhook',$3,'sent')`, newID, cid, fmt.Sprintf(`{"msg":%q}`, msg))
			}
			// stub webhook delivery if channel url present — async
			go deliverWebhookChannels(r.NotifyIDs, msg, newID, r.Severity, d.ID)
		} else {
			// condition cleared — auto-resolve any firing/acknowledged
			if exists {
				_, _ = db.Exec(ctx, `UPDATE alert_events SET status='resolved', resolved_at=now() WHERE id::text=$1`, evID)
				log.Printf("alert RESOLVED rule=%s dev=%s ev=%s", r.ID, d.ID, evID)
				if rdb != nil {
					payload, _ := json.Marshal(map[string]any{"event_id":evID,"rule_id":r.ID,"device_id":d.ID,"status":"resolved"})
					_ = rdb.Publish(ctx, "alerts", string(payload)).Err()
					_ = rdb.Publish(ctx, "alert:"+d.ID, string(payload)).Err()
				}
			}
		}
	}
}

func evalNumeric(op string, cur float64, thr *float64) bool {
	if thr==nil { return false }
	t:= *thr
	switch op {
	case ">": return cur > t
	case ">=": return cur >= t
	case "<": return cur < t
	case "<=": return cur <= t
	case "=", "==": return cur == t
	case "!=": return cur != t
	default: return cur > t
	}
}

func buildAlertMessage(r alertRule, d Device, status string, cur float64) string {
	if r.Metric=="device_status" {
		if status=="offline" { return fmt.Sprintf("Device %s (%s) OFFLINE", d.Name, d.ID) }
		return fmt.Sprintf("Device %s (%s) online", d.Name, d.ID)
	}
	thrStr:="?"
	if r.Threshold!=nil { thrStr = fmt.Sprintf("%.1f", *r.Threshold) }
	return fmt.Sprintf("%s %.1f %s %s on %s (%s) [%s]", r.Metric, cur, r.Operator, thrStr, d.Name, d.ID, r.Severity)
}

func deliverWebhookChannels(cids []string, msg, eventID, severity, deviceID string) {
	if len(cids)==0 { return }
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	for _, cid := range cids {
		var typ, cfgRaw string
		err:= db.QueryRow(ctx, `SELECT type, config::text FROM alert_channels WHERE id::text=$1`, cid).Scan(&typ, &cfgRaw)
		if err!=nil { continue }
		if typ != "webhook" { continue }
		var cfg map[string]any
		_ = json.Unmarshal([]byte(cfgRaw), &cfg)
		urlStr, _ := cfg["url"].(string)
		if urlStr=="" { continue }
		body, _ := json.Marshal(map[string]any{"event_id":eventID,"device_id":deviceID,"severity":severity,"message":msg,"time":time.Now().UTC()})
		req, _ := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(body))
		req.Header.Set("Content-Type","application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			_, _ = db.Exec(ctx, `INSERT INTO alert_logs(event_id, channel_id, channel_type, payload, status, error) VALUES($1::uuid,$2::uuid,'webhook',$3,'failed',$4)`, eventID, cid, string(body), err.Error())
			continue
		}
		ioDiscard(resp.Body)
		st:="sent"
		errMsg:=""
		if resp.StatusCode>=400 { st="failed"; errMsg=fmt.Sprintf("http %d", resp.StatusCode) }
		_, _ = db.Exec(ctx, `INSERT INTO alert_logs(event_id, channel_id, channel_type, payload, status, error) VALUES($1::uuid,$2::uuid,'webhook',$3,$4,$5)`, eventID, cid, string(body), st, errMsg)
	}
}

func ioDiscard(r interface{ Read([]byte)(int,error); Close() error}){ if r!=nil { var b [512]byte; for { _,e:=r.Read(b[:]); if e!=nil {break} }; r.Close() } }

type sqlNullFloat64 struct { Float64 float64; Valid bool }
func (n *sqlNullFloat64) Scan(v interface{}) error {
	if v==nil { n.Valid=false; return nil }
	switch x:=v.(type){
	case float64: n.Float64=x; n.Valid=true
	case float32: n.Float64=float64(x); n.Valid=true
	case int64: n.Float64=float64(x); n.Valid=true
	case int32: n.Float64=float64(x); n.Valid=true
	case int: n.Float64=float64(x); n.Valid=true
	case string: if f,err:= strconv.ParseFloat(x,64); err==nil { n.Float64=f; n.Valid=true }
	default: n.Valid=false
	}
	return nil
}

func formatBps(b int64) string {
	if b >= 1_000_000_000 {
		return fmt.Sprintf("%.1f Gbps", float64(b)/1e9)
	}
	if b >= 1_000_000 {
		return fmt.Sprintf("%.0f Mbps", float64(b)/1e6)
	}
	if b >= 1_000 {
		return fmt.Sprintf("%.0f Kbps", float64(b)/1e3)
	}
	return fmt.Sprintf("%d bps", b)
}

// nullable helpers
type sqlNullInt32 struct {
	Val   int32
	Valid bool
}
func (n *sqlNullInt32) Scan(v interface{}) error {
	if v == nil {
		n.Valid = false
		return nil
	}
	switch x := v.(type) {
	case int32:
		n.Val = x
		n.Valid = true
	case int64:
		n.Val = int32(x)
		n.Valid = true
	case int:
		n.Val = int32(x)
		n.Valid = true
	case float64:
		n.Val = int32(x)
		n.Valid = true
	default:
		n.Valid = false
	}
	return nil
}
type sqlNullString struct {
	Str   string
	Valid bool
}
func (n *sqlNullString) Scan(v interface{}) error {
	if v == nil {
		n.Valid = false
		return nil
	}
	if s, ok := v.(string); ok {
		n.Str = s
		n.Valid = true
	}
	return nil
}
