package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/go-ping/ping"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func envOr(k, d string) string { if v:=os.Getenv(k); v!="" { return v }; return d }

var (
	db  *pgxpool.Pool
	rdb *redis.Client
)

type Device struct {
	ID   string
	IP   string
	Name string
}

func main(){
	dsn := envOr("DATABASE_URL","postgres://nms:nms_secret_change_me@db:5432/nms?sslmode=disable")
	redisURL := envOr("REDIS_URL","redis://redis:6379")
	globalPoll := 30
	fmt.Sscanf(envOr("GLOBAL_POLL_INTERVAL","30"), "%d", &globalPoll)

	var err error
	for i:=0;i<30;i++{
		db, err = pgxpool.New(context.Background(), dsn)
		if err==nil {
			if err=db.Ping(context.Background()); err==nil { break }
		}
		log.Printf("poller wait db %d/30: %v", i+1, err)
		time.Sleep(2*time.Second)
	}
	if err!=nil { log.Fatal("poller db fail:", err) }
	defer db.Close()
	log.Println("poller DB connected")

	opt,_:= redis.ParseURL(redisURL)
	rdb = redis.NewClient(opt)
	if err:=rdb.Ping(context.Background()).Err(); err!=nil { log.Println("poller redis warn:", err) } else { log.Println("poller Redis ok") }

	// immediate first poll then ticker
	go pollOnce(globalPoll)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		pollOnce(globalPoll)
	}
}

func pollOnce(global int){
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.Query(ctx, `SELECT d.id, d.ip, d.name FROM devices d`)
	if err!=nil { log.Println("poll query fail:", err); return }
	defer rows.Close()
	var devices []Device
	for rows.Next(){
		var id, ip, name string
		if err:= rows.Scan(&id,&ip,&name); err!=nil { continue }
		devices = append(devices, Device{ID:id, IP:ip, Name:name})
	}
	if len(devices)==0 { log.Println("poller: no devices"); return }
	log.Printf("poller: %d devices (global %ds)", len(devices), global)

	var wg sync.WaitGroup
	for _, dev := range devices {
		wg.Add(1)
		go func(d Device){
			defer wg.Done()
			lat, loss, ok := doPing(d.IP)
			status := "offline"
			if ok { status="online" }
			cpu := 20 + rand.Float64()*50
			mem := 30 + rand.Float64()*40
			_, _ = db.Exec(context.Background(),
				`INSERT INTO device_metrics(time, device_id, latency_ms, packet_loss, cpu, mem) VALUES(now(),$1,$2,$3,$4,$5)`,
				d.ID, lat, loss, cpu, mem)
			_, _ = db.Exec(context.Background(), `UPDATE devices SET status=$1 WHERE id=$2`, status, d.ID)
			if ok {
				rows2,_:= db.Query(context.Background(), `SELECT id, capacity FROM links WHERE sensor_device_id=$1`, d.ID)
				if rows2!=nil {
					for rows2.Next(){
						var lid string; var capv int64
						rows2.Scan(&lid,&capv)
						pct := 0.15 + rand.Float64()*0.7
						bpsVal := int64(float64(capv)*pct)
						bpsStr := formatBps(bpsVal)
						_,_ = db.Exec(context.Background(), `UPDATE links SET bps=$1 WHERE id=$2`, bpsStr, lid)
						inb := bpsVal
						outb := int64(float64(bpsVal)* (0.6+rand.Float64()*0.5))
						_,_ = db.Exec(context.Background(), `INSERT INTO link_metrics(time, link_id, in_bps, out_bps) VALUES(now(),$1,$2,$3)`, lid, inb, outb)
						if rdb!=nil {
							_ = rdb.Publish(context.Background(), "link:"+lid, fmt.Sprintf(`{"bps":"%s","in":%d,"out":%d}`, bpsStr, inb, outb)).Err()
						}
					}
					rows2.Close()
				}
			}
			log.Printf(" poll %s %s -> %s lat=%.1fms loss=%.0f%%", d.ID, d.IP, status, lat, loss)
		}(dev)
	}
	wg.Wait()
}

func doPing(ip string) (lat float64, loss float64, ok bool){
	p, err := ping.NewPinger(ip)
	if err!=nil { return 0,100,false }
	p.Count=2
	p.Timeout=2*time.Second
	p.SetPrivileged(false)
	err = p.Run()
	if err!=nil { return 0,100,false }
	stats:= p.Statistics()
	if stats.PacketsRecv==0 { return 0,100,false }
	return float64(stats.AvgRtt.Microseconds())/1000.0, float64(stats.PacketLoss), true
}

func formatBps(b int64) string {
	if b>=1_000_000_000 { return fmt.Sprintf("%.1f Gbps", float64(b)/1e9) }
	if b>=1_000_000 { return fmt.Sprintf("%.0f Mbps", float64(b)/1e6) }
	if b>=1_000 { return fmt.Sprintf("%.0f Kbps", float64(b)/1e3) }
	return fmt.Sprintf("%d bps", b)
}
