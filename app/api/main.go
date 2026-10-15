package main

import (
	"bufio"
	"bytes"
	"sync"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"github.com/go-ping/ping"
	"github.com/gosnmp/gosnmp"
)

var (
	db  *pgxpool.Pool
	rdb *redis.Client
	jwtKey []byte
	wsUpgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }, ReadBufferSize: 4096, WriteBufferSize: 4096}
)

type ctxKey string
const userCtxKey ctxKey = "user"

func envOr(k, d string) string { if v:=os.Getenv(k); v!="" { return v }; return d }

func main(){
	dsn := envOr("DATABASE_URL","postgres://nms:nms_secret_change_me@db:5432/nms?sslmode=disable")
	redisURL := envOr("REDIS_URL","redis://redis:6379")
	jwtSecret := envOr("JWT_SECRET","")
	if jwtSecret=="" { b:=make([]byte,32); rand.Read(b); jwtSecret = fmt.Sprintf("%x",b); log.Println("JWT_SECRET generated ephemeral") }
	jwtKey = []byte(jwtSecret)
	port := envOr("PORT","8080")
	defUser := envOr("DEFAULT_SUPERADMIN_USER","superadmin")
	defPass := envOr("DEFAULT_SUPERADMIN_PASS","Admin@123")

	// wait db
	var err error
	for i:=0;i<30;i++{
		db, err = pgxpool.New(context.Background(), dsn)
		if err==nil { if err=db.Ping(context.Background()); err==nil { break } }
		log.Printf("wait db %d/30: %v", i+1, err)
		time.Sleep(2*time.Second)
	}
	if err!=nil { log.Fatal("db connect fail:", err) }
	defer db.Close()
	log.Println("DB connected")

	// redis
	opt, err := redis.ParseURL(redisURL)
	if err!=nil { log.Fatal("redis url:", err) }
	rdb = redis.NewClient(opt)
	if err:=rdb.Ping(context.Background()).Err(); err!=nil { log.Println("redis ping fail:", err) } else { log.Println("Redis connected") }

	// migrations
	if err:=runMigrations(); err!=nil { log.Println("migrations warn:", err) } else { log.Println("migrations ok") }
	if err:=ensureSuperadmin(defUser, defPass); err!=nil { log.Println("ensure superadmin warn:", err) } else { log.Printf("superadmin ensured (%s)", defUser) }

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET","POST","PUT","PATCH","DELETE","OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: false,
		MaxAge: 300,
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request){
		jsonWrite(w, map[string]any{"ok":true,"time":time.Now().UTC(),"db": dbStat()})
	})
	// public
	r.Get("/api/setup-status", handleSetupStatus)
	r.Post("/api/auth/login", handleLogin)
	r.Post("/api/auth/change-password", handleChangePassword) // allows firstSetup without token if must_change

	// protected
	r.Group(func(pr chi.Router){
		pr.Use(authMiddleware)
		pr.Get("/api/me", handleMe)
		// device groups
		pr.Get("/api/device-groups", handleListGroups)
		pr.Post("/api/device-groups", handleCreateGroup)
		pr.Put("/api/device-groups/{id}", handleUpdateGroup)
		pr.Delete("/api/device-groups/{id}", handleDeleteGroup)
		// racks
		pr.Get("/api/racks", handleListRacks)
		pr.Post("/api/racks", handleCreateRack)
		pr.Put("/api/racks/{id}", handleUpdateRack)
		pr.Delete("/api/racks/{id}", handleDeleteRack)
		// devices
		pr.Get("/api/devices", handleListDevices)
		pr.Post("/api/devices", handleCreateDevice)
		pr.Get("/api/devices/{id}", handleGetDevice)
		pr.Put("/api/devices/{id}", handleUpdateDevice)
		pr.Delete("/api/devices/{id}", handleDeleteDevice)
		pr.Post("/api/devices/test-connection", handleTestDeviceConnection)
		pr.Post("/api/devices/{id}/test", handleTestDeviceConnection)
		// bootstrap — single roundtrip for devices page (priority render)
		pr.Get("/api/bootstrap", handleBootstrap)
		// links
		pr.Get("/api/links", handleListLinks)
		pr.Post("/api/links", handleCreateLink)
		pr.Put("/api/links/{id}", handleUpdateLink)
		pr.Delete("/api/links/{id}", handleDeleteLink)
		// interfaces
		pr.Get("/api/interfaces", handleListInterfaces)
		pr.Post("/api/interfaces", handleCreateInterface)
		// topology
		pr.Get("/api/topology", handleTopology)
		// users & roles
		pr.Get("/api/users", handleListUsers)
		pr.Post("/api/users", handleCreateUser)
		pr.Put("/api/users/{id}", handleUpdateUser)
		pr.Delete("/api/users/{id}", handleDeleteUser)
		pr.Get("/api/roles", handleListRoles)
		pr.Post("/api/roles", handleCreateRole)
		pr.Put("/api/roles/{id}", handleUpdateRole)
		pr.Delete("/api/roles/{id}", handleDeleteRole)
		pr.Get("/api/roles/{id}/groups", handleGetRoleGroups)
		pr.Get("/api/role-groups", handleListRoleGroupsDetail)
		// metrics
		pr.Get("/api/metrics/device/{id}", handleDeviceMetrics)
		pr.Get("/api/metrics/link/{id}", handleLinkMetrics)
		// alerts P1
		pr.Get("/api/alert-rules", handleListAlertRules)
		pr.Post("/api/alert-rules", handleCreateAlertRule)
		pr.Put("/api/alert-rules/{id}", handleUpdateAlertRule)
		pr.Delete("/api/alert-rules/{id}", handleDeleteAlertRule)
		pr.Get("/api/alert-events", handleListAlertEvents)
		pr.Get("/api/alerts", handleListAlertEvents)
		pr.Post("/api/alert-events/{id}/ack", handleAckAlert)
		pr.Post("/api/alert-events/{id}/resolve", handleResolveAlert)
		pr.Post("/api/alerts/{id}/ack", handleAckAlert)
		pr.Post("/api/alerts/{id}/resolve", handleResolveAlert)
		pr.Get("/api/alert-channels", handleListAlertChannels)
		pr.Post("/api/alert-channels", handleCreateAlertChannel)
		pr.Put("/api/alert-channels/{id}", handleUpdateAlertChannel)
		pr.Delete("/api/alert-channels/{id}", handleDeleteAlertChannel)
		pr.Get("/api/alerts/summary", handleAlertSummary)
		// bastion
		pr.Get("/api/bastion/ssh", handleBastionSSH)
		pr.Get("/api/bastion/telnet", handleBastionTelnet)
		pr.Get("/api/bastion/mikrotik-api", handleMikrotikAPI)
		pr.Get("/api/bastion/webfig", handleWebfigProxy)
		pr.Get("/api/bastion/webfig-proxy", handleWebfigProxy)
		pr.Get("/api/bastion/webconsole/{id}", handleWebConsole)
	})

	log.Printf("API listen :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func dbStat() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var one int
	if err:= db.QueryRow(ctx, "SELECT 1").Scan(&one); err!=nil { return err.Error() }
	return "ok"
}

func runMigrations() error {
	cands := []string{"/migrations","./migrations","migrations", filepath.Join("..","..","migrations")}
	var dir string
	for _, c := range cands { if _, err:= os.Stat(c); err==nil { dir=c; break } }
	if dir=="" { return fmt.Errorf("migrations dir not found") }
	entries, err := os.ReadDir(dir)
	if err!=nil { return err }
	ctx := context.Background()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") { continue }
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		sql := string(b)
		if _, err:= db.Exec(ctx, sql); err!=nil {
			log.Printf("migration %s warn: %v", e.Name(), err)
		} else {
			log.Printf("migration %s ok", e.Name())
		}
	}
	return nil
}

func ensureSuperadmin(user, pass string) error {
	ctx := context.Background()
	var exists string
	err := db.QueryRow(ctx, "SELECT username FROM users WHERE username=$1", user).Scan(&exists)
	hash, _ := bcrypt.GenerateFromPassword([]byte(pass), 10)
	if err==sql.ErrNoRows || err!=nil && strings.Contains(err.Error(),"no rows") {
		_, err = db.Exec(ctx, `INSERT INTO users(username,password_hash,must_change_password,role_id)
		 VALUES($1,$2,true,(SELECT id FROM roles WHERE name='SuperAdmin' LIMIT 1))`, user, string(hash))
		return err
	}
	// exists: check must_change and hash placeholder
	var phash string; var must bool
	_ = db.QueryRow(ctx, "SELECT password_hash,must_change_password FROM users WHERE username=$1", user).Scan(&phash, &must)
	if strings.Contains(phash,"placeholder") || must {
		// keep must_change true if was true, but update hash if placeholder
		if strings.Contains(phash,"placeholder") {
			_, err = db.Exec(ctx, "UPDATE users SET password_hash=$1, must_change_password=true WHERE username=$2", string(hash), user)
			return err
		}
	}
	return nil
}

// helpers
func jsonWrite(w http.ResponseWriter, v any){ w.Header().Set("Content-Type","application/json"); json.NewEncoder(w).Encode(v) }
func jsonError(w http.ResponseWriter, code int, msg string){ w.Header().Set("Content-Type","application/json"); w.WriteHeader(code); json.NewEncoder(w).Encode(map[string]any{"error":msg}) }
func readJSON(r *http.Request, v any) error { defer r.Body.Close(); return json.NewDecoder(r.Body).Decode(v) }

// auth
func handleSetupStatus(w http.ResponseWriter, r *http.Request){
	defUser := envOr("DEFAULT_SUPERADMIN_USER","superadmin")
	var must bool
	var exists bool
	err := db.QueryRow(r.Context(), "SELECT must_change_password FROM users WHERE username=$1", defUser).Scan(&must)
	if err!=nil { exists=false; must=true } else { exists=true }
	first := must // jika must_change true -> firstSetup
	jsonWrite(w, map[string]any{
		"firstSetup": first,
		"exists": exists,
		"defaultUser": defUser,
		"defaultPass": func() string { if first { return envOr("DEFAULT_SUPERADMIN_PASS","Admin@123") }; return "" }(),
		"must_change_password": must,
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request){
	var req struct{ Username string `json:"username"`; Password string `json:"password"` }
	if err:= readJSON(r, &req); err!=nil { jsonError(w,400,"bad json"); return }
	var id, phash string; var must bool; var roleID sql.NullString
	err := db.QueryRow(r.Context(), "SELECT id::text,password_hash,must_change_password,role_id::text FROM users WHERE username=$1", req.Username).Scan(&id,&phash,&must,&roleID)
	if err!=nil { jsonError(w,401,"invalid credentials"); return }
	if err:= bcrypt.CompareHashAndPassword([]byte(phash), []byte(req.Password)); err!=nil { jsonError(w,401,"invalid credentials"); return }
	// issue jwt
	claims := jwt.MapClaims{"sub":id,"username":req.Username,"role":roleID.String,"exp": time.Now().Add(24*time.Hour).Unix()}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, _ := tok.SignedString(jwtKey)
	jsonWrite(w, map[string]any{"token":ss,"must_change_password":must,"user":map[string]any{"id":id,"username":req.Username,"role_id":roleID.String}})
}

func handleChangePassword(w http.ResponseWriter, r *http.Request){
	var req struct{ Username string `json:"username"`; OldPassword string `json:"old_password"`; NewPassword string `json:"new_password"`; Token string `json:"token"` }
	_ = readJSON(r, &req)
	// try bearer first
	var username string
	if tok:= bearer(r); tok!="" {
		if claims, ok := parseToken(tok); ok { username = claims["username"].(string) }
	}
	if username=="" { username = req.Username }
	if username=="" { jsonError(w,400,"username required"); return }
	if req.NewPassword=="" || len(req.NewPassword)<6 { jsonError(w,400,"new_password min 6"); return }
	var phash string; var uid string
	err := db.QueryRow(r.Context(), "SELECT id::text,password_hash FROM users WHERE username=$1", username).Scan(&uid,&phash)
	if err!=nil { jsonError(w,404,"user not found"); return }
	// if has token, skip old check; else check old
	if bearer(r)=="" {
		if req.OldPassword=="" { jsonError(w,400,"old_password required"); return }
		if err:= bcrypt.CompareHashAndPassword([]byte(phash), []byte(req.OldPassword)); err!=nil { jsonError(w,401,"old password wrong"); return }
	} else {
		// verify old if provided but not required
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 10)
	_, err = db.Exec(r.Context(), "UPDATE users SET password_hash=$1, must_change_password=false, mikrotik_sync_status='pending' WHERE id::text=$2", string(hash), uid)
	if err!=nil { jsonError(w,500, err.Error()); return }
	jsonWrite(w, map[string]any{"ok":true})
}

func bearer(r *http.Request) string {
	h:= r.Header.Get("Authorization")
	if strings.HasPrefix(h,"Bearer ") { return strings.TrimPrefix(h,"Bearer ") }
	if t:= r.URL.Query().Get("token"); t!="" { return t }
	if c:= r.URL.Query().Get("access_token"); c!="" { return c }
	return ""
}
func parseToken(ss string) (jwt.MapClaims,bool){
	tok, err := jwt.Parse(ss, func(t *jwt.Token) (any,error){ return jwtKey, nil })
	if err!=nil || !tok.Valid { return nil,false }
	if c, ok:= tok.Claims.(jwt.MapClaims); ok { return c,true }
	return nil,false
}
func authMiddleware(next http.Handler) http.Handler{
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
		tok:= bearer(r)
		if tok=="" { jsonError(w,401,"missing token"); return }
		claims, ok:= parseToken(tok)
		if !ok { jsonError(w,401,"invalid token"); return }
		ctx := context.WithValue(r.Context(), userCtxKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func handleMe(w http.ResponseWriter, r *http.Request){
	claims := r.Context().Value(userCtxKey).(jwt.MapClaims)
	jsonWrite(w, claims)
}

// --- RBAC 3 dimensi ---
type AuthUser struct {
	ID string
	Username string
	RoleID string
	RoleName string
	MikrotikGroup string
	Menu map[string]bool
	AllowedGroupIDs []string
}

func getAuthUser(ctx context.Context) (*AuthUser, error){
	v := ctx.Value(userCtxKey)
	if v==nil { return nil, fmt.Errorf("no auth") }
	claims, ok := v.(jwt.MapClaims)
	if !ok { return nil, fmt.Errorf("bad claims") }
	uid, _ := claims["sub"].(string)
	uname, _ := claims["username"].(string)
	// query role
	var roleID, roleName, mkGroup sql.NullString
	var menuRaw sql.NullString
	err := db.QueryRow(ctx, `SELECT u.role_id::text, r.name, r.mikrotik_group, r.menu_permissions::text
		FROM users u LEFT JOIN roles r ON r.id=u.role_id WHERE u.id::uuid=$1::uuid`, uid).Scan(&roleID, &roleName, &mkGroup, &menuRaw)
	menu := map[string]bool{}
	if err==nil && menuRaw.Valid && len(menuRaw.String)>0 {
		_ = json.Unmarshal([]byte(menuRaw.String), &menu)
	}
	au := &AuthUser{ID:uid, Username:uname, RoleID: roleID.String, RoleName: roleName.String, MikrotikGroup: mkGroup.String, Menu: menu}
	// allowed groups
	if roleID.Valid && roleID.String!="" {
		rows,_:= db.Query(ctx, `SELECT group_id::text FROM role_device_groups WHERE role_id::uuid=$1::uuid`, roleID.String)
		if rows!=nil {
			for rows.Next(){ var gid string; rows.Scan(&gid); au.AllowedGroupIDs=append(au.AllowedGroupIDs, gid) }
			rows.Close()
		}
	}
	// SuperAdmin with menu settings true -> allow all (empty list means all)
	return au, nil
}

func requireMenu(w http.ResponseWriter, r *http.Request, key string) (*AuthUser,bool){
	au, err := getAuthUser(r.Context())
	if err!=nil { jsonError(w,401,"auth required"); return nil,false }
	// SuperAdmin bypass
	if au.RoleName=="SuperAdmin" { return au,true }
	if v,ok:=au.Menu[key]; ok && v { return au,true }
	jsonError(w,403,"forbidden: missing menu "+key)
	return nil,false
}

func allowedGroupPlaceholders(au *AuthUser) (string, []any){
	if au==nil || len(au.AllowedGroupIDs)==0 {
		// if no restriction -> allow all (no filter)
		// distinguish SuperAdmin: allow all; otherwise also allow all for demo
		return "", nil
	}
	// build $1,$2...
	ph := []string{}
	args := []any{}
	for i,gid:=range au.AllowedGroupIDs {
		ph=append(ph, fmt.Sprintf("$%d", i+1))
		args=append(args, gid)
	}
	return "(" + strings.Join(ph,",") + ")", args
}


// generic CRUD helpers
func handleListGroups(w http.ResponseWriter, r *http.Request){
	au,_:= getAuthUser(r.Context())
	query := "SELECT id::text,name,description,poll_interval FROM device_groups"
	args := []any{}
	if au!=nil && len(au.AllowedGroupIDs)>0 {
		ph, a := allowedGroupPlaceholders(au)
		query += " WHERE id::text IN "+ph
		args = a
	}
	query += " ORDER BY name"
	rows, _ := db.Query(r.Context(), query, args...)
	defer func(){ if rows!=nil { rows.Close() } }()
	var out []map[string]any
	for rows!=nil && rows.Next(){
		var id,name,desc string; var pi int
		rows.Scan(&id,&name,&desc,&pi)
		out = append(out, map[string]any{"id":id,"name":name,"description":desc,"poll_interval":pi})
	}
	if out==nil { out = []map[string]any{} }
	jsonWrite(w, out)
}
func handleCreateGroup(w http.ResponseWriter, r *http.Request){
	if au,_:=getAuthUser(r.Context()); au!=nil && au.RoleName!="SuperAdmin" {
		if !au.Menu["settings"] && !au.Menu["users"] { jsonError(w,403,"forbidden: settings/users"); return }
	}
	var req struct{ Name string `json:"name"`; Description string `json:"description"`; PollInterval *int `json:"poll_interval"`}
	if err:=readJSON(r,&req); err!=nil||req.Name==""{ jsonError(w,400,"name required"); return}
	pi:=30; if req.PollInterval!=nil { pi=*req.PollInterval }
	var id string
	err:= db.QueryRow(r.Context(), "INSERT INTO device_groups(name,description,poll_interval) VALUES($1,$2,$3) RETURNING id::text", req.Name, req.Description, pi).Scan(&id)
	if err!=nil{ jsonError(w,500,err.Error()); return }
	jsonWrite(w, map[string]any{"id":id,"name":req.Name})
}
func handleUpdateGroup(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req map[string]any; _=readJSON(r,&req)
	// simple update
	if n,ok:=req["name"].(string); ok {
		_, _ = db.Exec(r.Context(), "UPDATE device_groups SET name=$1 WHERE id::text=$2", n, id)
	}
	if d,ok:=req["description"].(string); ok {
		_, _ = db.Exec(r.Context(), "UPDATE device_groups SET description=$1 WHERE id::text=$2", d, id)
	}
	if pi,ok:=req["poll_interval"]; ok {
		if f,ok:=pi.(float64); ok { _, _ = db.Exec(r.Context(), "UPDATE device_groups SET poll_interval=$1 WHERE id::text=$2", int(f), id) }
	}
	jsonWrite(w, map[string]any{"ok":true})
}
func handleDeleteGroup(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	_, _ = db.Exec(r.Context(), "DELETE FROM device_groups WHERE id::text=$1", id)
	jsonWrite(w, map[string]any{"ok":true})
}

func handleListRacks(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"dashboard"); !ok { return }
	rows,_:= db.Query(r.Context(),"SELECT id,name,location,x,y FROM racks ORDER BY name")
	defer func(){ if rows!=nil{rows.Close()} }()
	var out []map[string]any
	for rows!=nil&&rows.Next(){
		var id,name,loc string; var x,y int
		rows.Scan(&id,&name,&loc,&x,&y)
		out=append(out, map[string]any{"id":id,"name":name,"location":loc,"x":x,"y":y})
	}
	if out==nil{ out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateRack(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	var req struct{ ID string `json:"id"`; Name string `json:"name"`; Location string `json:"location"`; X *int `json:"x"`; Y *int `json:"y"`}
	if err:=readJSON(r,&req); err!=nil||req.Name==""{ jsonError(w,400,"name required"); return}
	if req.ID=="" { req.ID = strings.ToUpper(strings.ReplaceAll(req.Name," ","-")) }
	x,y:=0,0; if req.X!=nil{x=*req.X}; if req.Y!=nil{y=*req.Y}
	_,err:= db.Exec(r.Context(), "INSERT INTO racks(id,name,location,x,y) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name, location=EXCLUDED.location", req.ID, req.Name, req.Location, x,y)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	jsonWrite(w, map[string]any{"id":req.ID,"name":req.Name})
}
func handleUpdateRack(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req map[string]any; _=readJSON(r,&req)
	if n,ok:=req["name"].(string); ok { _, _= db.Exec(r.Context(),"UPDATE racks SET name=$1 WHERE id=$2", n, id) }
	if l,ok:=req["location"].(string); ok { _, _= db.Exec(r.Context(),"UPDATE racks SET location=$1 WHERE id=$2", l, id) }
	if x,ok:=req["x"].(float64); ok { _, _= db.Exec(r.Context(),"UPDATE racks SET x=$1 WHERE id=$2", int(x), id) }
	if y,ok:=req["y"].(float64); ok { _, _= db.Exec(r.Context(),"UPDATE racks SET y=$1 WHERE id=$2", int(y), id) }
	jsonWrite(w,map[string]any{"ok":true})
}
func handleDeleteRack(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	_, _= db.Exec(r.Context(),"DELETE FROM racks WHERE id=$1", id)
	jsonWrite(w,map[string]any{"ok":true})
}

func handleListDevices(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"dashboard"); !ok { return }
	au,_:= getAuthUser(r.Context())
	query := `SELECT id,name,ip,type,rack_id,group_id::text,status,x,y,poll_interval,snmp_community,snmp_version,ssh_user,ssh_port,api_user,telnet_port,note FROM devices`
	args := []any{}
	if au!=nil && len(au.AllowedGroupIDs)>0 {
		ph, a := allowedGroupPlaceholders(au)
		query += ` WHERE group_id::text IN `+ph
		args = a
	}
	query += ` ORDER BY name`
	rows,_:= db.Query(r.Context(), query, args...)
	defer func(){ if rows!=nil{rows.Close()} }()
	var out []map[string]any
	for rows!=nil&&rows.Next(){
		var id,name,ip,typ,status,comm,sver,sshU,apiU,note string
		var x,y int
		var pi,sp,tp sql.NullInt32
		var rackNull, groupNull, sshUNull, apiUNull, noteNull sql.NullString
		var sverNull, commNull sql.NullString
		// scan in same order as SELECT
		rows.Scan(&id,&name,&ip,&typ,&rackNull,&groupNull,&status,&x,&y,&pi,&commNull,&sverNull,&sshUNull,&sp,&apiUNull,&tp,&noteNull)
		rack:=rackNull.String; group:=groupNull.String; comm=commNull.String; sver=sverNull.String; sshU=sshUNull.String; apiU=apiUNull.String; note=noteNull.String
		piv:=any(nil); if pi.Valid{ piv=pi.Int32 }
		spv:=22; if sp.Valid && sp.Int32>0 { spv=int(sp.Int32) }
		tpv:=23; if tp.Valid && tp.Int32>0 { tpv=int(tp.Int32) }
		out=append(out, map[string]any{"id":id,"name":name,"ip":ip,"type":typ,"rack_id":rack,"group_id":group,"status":status,"x":x,"y":y,"poll_interval":piv,"snmp_community":comm,"snmp_version":sver,"ssh_user":sshU,"ssh_port":spv,"api_user":apiU,"telnet_port":tpv,"note":note})
	}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateDevice(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	var req struct{ ID string `json:"id"`; Name string `json:"name"`; IP string `json:"ip"`; Type string `json:"type"`; RackID *string `json:"rack_id"`; GroupID *string `json:"group_id"`; PollInterval *int `json:"poll_interval"`; X *int `json:"x"`; Y *int `json:"y"`; SNMPCommunity *string `json:"snmp_community"`; SNMPVersion *string `json:"snmp_version"`; SSHUser *string `json:"ssh_user"`; SSHPass *string `json:"ssh_pass"`; SSHPort *int `json:"ssh_port"`; APIUser *string `json:"api_user"`; APIPass *string `json:"api_pass"`; TelnetPort *int `json:"telnet_port"`; Note *string `json:"note"`}
	if err:=readJSON(r,&req); err!=nil||req.Name==""||req.IP==""{ jsonError(w,400,"name and ip required"); return}
	if req.ID==""{ req.ID = strings.ToUpper(strings.ReplaceAll(req.Name," ","-")) + fmt.Sprintf("-%d", time.Now().Unix()%10000) }
	if req.Type==""{ req.Type="mikrotik" }
	x,y:=100,100; if req.X!=nil{x=*req.X}; if req.Y!=nil{y=*req.Y}
	var gid any; if req.GroupID!=nil && *req.GroupID!=""{gid=*req.GroupID} else { gid=nil }
	var rid any; if req.RackID!=nil && *req.RackID!=""{rid=*req.RackID} else { rid=nil }
	var pi any; if req.PollInterval!=nil{pi=*req.PollInterval} else {pi=nil}
	comm:="public"; if req.SNMPCommunity!=nil { comm=*req.SNMPCommunity }
	sver:="2c"; if req.SNMPVersion!=nil && *req.SNMPVersion!="" { sver=*req.SNMPVersion }
	sshU:=""; if req.SSHUser!=nil { sshU=*req.SSHUser }
	sshP:=""; if req.SSHPass!=nil { sshP=*req.SSHPass }
	sshPort:=22; if req.SSHPort!=nil && *req.SSHPort>0 { sshPort=*req.SSHPort }
	apiU:=""; if req.APIUser!=nil { apiU=*req.APIUser }
	apiP:=""; if req.APIPass!=nil { apiP=*req.APIPass }
	tport:=23; if req.TelnetPort!=nil && *req.TelnetPort>0 { tport=*req.TelnetPort }
	note:=""; if req.Note!=nil { note=*req.Note }
	_,err:= db.Exec(r.Context(), `INSERT INTO devices(id,name,ip,type,rack_id,group_id,poll_interval,x,y,snmp_community,snmp_version,ssh_user,ssh_pass,ssh_port,api_user,api_pass,telnet_port,note) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		req.ID, req.Name, req.IP, req.Type, rid, gid, pi, x,y, comm, sver, sshU, sshP, sshPort, apiU, apiP, tport, note)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	jsonWrite(w, map[string]any{"id":req.ID})
}
func handleGetDevice(w http.ResponseWriter, r *http.Request){
	id:= chi.URLParam(r,"id")
	var name,ip,typ,status,comm,sver,sshU,sshP,apiU,apiP,note string; var x,y,sp,tp int
	var pi sql.NullInt32
	var gid,rid sql.NullString
	err:= db.QueryRow(r.Context(),"SELECT name,ip,type,status,x,y,poll_interval,group_id::text,rack_id,snmp_community,snmp_version,ssh_user,ssh_pass,ssh_port,api_user,api_pass,telnet_port,note FROM devices WHERE id=$1", id).Scan(&name,&ip,&typ,&status,&x,&y,&pi,&gid,&rid,&comm,&sver,&sshU,&sshP,&sp,&apiU,&apiP,&tp,&note)
	if err!=nil{ jsonError(w,404,"not found"); return}
	piv:=any(nil); if pi.Valid{ piv=pi.Int32 }
	jsonWrite(w, map[string]any{"id":id,"name":name,"ip":ip,"type":typ,"status":status,"x":x,"y":y,"poll_interval":piv,"group_id":gid.String,"rack_id":rid.String,"snmp_community":comm,"snmp_version":sver,"ssh_user":sshU,"ssh_pass":sshP,"ssh_port":sp,"api_user":apiU,"api_pass":apiP,"telnet_port":tp,"note":note})
}
func handleUpdateDevice(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req map[string]any; _=readJSON(r,&req)
	if n,ok:=req["name"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET name=$1 WHERE id=$2", n,id)}
	if ip,ok:=req["ip"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET ip=$1 WHERE id=$2", ip,id)}
	if t,ok:=req["type"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET type=$1 WHERE id=$2", t,id)}
	if rid,ok:=req["rack_id"].(string); ok{
		if rid==""{ _,_=db.Exec(r.Context(),"UPDATE devices SET rack_id=NULL WHERE id=$1", id)} else { _,_=db.Exec(r.Context(),"UPDATE devices SET rack_id=$1 WHERE id=$2", rid,id)}
	}
	if gid,ok:=req["group_id"].(string); ok{
		if gid==""{ _,_=db.Exec(r.Context(),"UPDATE devices SET group_id=NULL WHERE id=$1", id)} else { _,_=db.Exec(r.Context(),"UPDATE devices SET group_id=$1 WHERE id=$2", gid,id)}
	}
	if x,ok:=req["x"].(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET x=$1 WHERE id=$2", int(x),id)}
	if y,ok:=req["y"].(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET y=$1 WHERE id=$2", int(y),id)}
	if v,ok:=req["poll_interval"]; ok{
		if v==nil { _,_=db.Exec(r.Context(),"UPDATE devices SET poll_interval=NULL WHERE id=$1", id)} else if pi,ok:=v.(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET poll_interval=$1 WHERE id=$2", int(pi),id)}
	}
	if v,ok:=req["snmp_community"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET snmp_community=$1 WHERE id=$2", v,id)}
	if v,ok:=req["snmp_version"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET snmp_version=$1 WHERE id=$2", v,id)}
	if v,ok:=req["ssh_user"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET ssh_user=$1 WHERE id=$2", v,id)}
	if v,ok:=req["ssh_pass"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET ssh_pass=$1 WHERE id=$2", v,id)}
	if v,ok:=req["ssh_port"].(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET ssh_port=$1 WHERE id=$2", int(v),id)}
	if v,ok:=req["api_user"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET api_user=$1 WHERE id=$2", v,id)}
	if v,ok:=req["api_pass"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET api_pass=$1 WHERE id=$2", v,id)}
	if v,ok:=req["telnet_port"].(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET telnet_port=$1 WHERE id=$2", int(v),id)}
	if v,ok:=req["note"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET note=$1 WHERE id=$2", v,id)}
	jsonWrite(w,map[string]any{"ok":true})
}
func handleDeleteDevice(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	_, _= db.Exec(r.Context(),"DELETE FROM devices WHERE id=$1", id)
	jsonWrite(w,map[string]any{"ok":true})
}

func handleTestDeviceConnection(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	var req struct{
		ID string `json:"id"`
		IP string `json:"ip"`
		SNMPCommunity *string `json:"snmp_community"`
		SNMPVersion *string `json:"snmp_version"`
		SSHUser *string `json:"ssh_user"`
		SSHPass *string `json:"ssh_pass"`
		SSHPort *int `json:"ssh_port"`
		APIUser *string `json:"api_user"`
		APIPass *string `json:"api_pass"`
		TelnetPort *int `json:"telnet_port"`
	}
	_ = readJSON(r,&req)
	idParam := chi.URLParam(r,"id")
	if req.ID=="" && idParam!="" { req.ID=idParam }
	// If ID present, fill missing from DB
	var dbIP, dbComm, dbVer, dbSSHU, dbSSHP, dbAPIU, dbAPIP sql.NullString
	var dbSP, dbTP sql.NullInt32
	if req.ID!="" {
		_ = db.QueryRow(r.Context(), `SELECT ip, snmp_community, snmp_version, ssh_user, ssh_pass, ssh_port, api_user, api_pass, telnet_port FROM devices WHERE id=$1`, req.ID).Scan(&dbIP,&dbComm,&dbVer,&dbSSHU,&dbSSHP,&dbSP,&dbAPIU,&dbAPIP,&dbTP)
		if req.IP=="" && dbIP.Valid { req.IP=dbIP.String }
		if req.SNMPCommunity==nil && dbComm.Valid && dbComm.String!="" { v:=dbComm.String; req.SNMPCommunity=&v }
		if req.SNMPVersion==nil && dbVer.Valid && dbVer.String!="" { v:=dbVer.String; req.SNMPVersion=&v }
		if req.SSHUser==nil && dbSSHU.Valid && dbSSHU.String!="" { v:=dbSSHU.String; req.SSHUser=&v }
		if req.SSHPass==nil && dbSSHP.Valid && dbSSHP.String!="" { v:=dbSSHP.String; req.SSHPass=&v }
		if req.SSHPort==nil && dbSP.Valid { v:=int(dbSP.Int32); if v>0 { req.SSHPort=&v } }
		if req.APIUser==nil && dbAPIU.Valid && dbAPIU.String!="" { v:=dbAPIU.String; req.APIUser=&v }
		if req.APIPass==nil && dbAPIP.Valid && dbAPIP.String!="" { v:=dbAPIP.String; req.APIPass=&v }
		if req.TelnetPort==nil && dbTP.Valid { v:=int(dbTP.Int32); if v>0 { req.TelnetPort=&v } }
	}
	if req.IP=="" { jsonError(w,400,"ip required"); return }
	community := "public"
	if req.SNMPCommunity!=nil && *req.SNMPCommunity!="" { community=*req.SNMPCommunity }
	snmpVer := "2c"
	if req.SNMPVersion!=nil && *req.SNMPVersion!="" { snmpVer=*req.SNMPVersion }
	// Use deviceCreds for consistent fallback logic (includes JWT username fallback)
	_, sshUser, sshPass, sshPort, _, _, _ := deviceCreds(r.Context(), req.ID)
	// Override with any explicitly provided values from request
	if req.SSHUser!=nil && *req.SSHUser!="" { sshUser = *req.SSHUser }
	if req.SSHPass!=nil { sshPass = *req.SSHPass }
	if req.SSHPort!=nil && *req.SSHPort>0 { sshPort = *req.SSHPort }
	// ping
	pingRes := map[string]any{"ok":false}
	func(){
		defer func(){ if r:=recover(); r!=nil { pingRes["error"]=fmt.Sprintf("%v",r) } }()
		for _, priv := range []bool{true,false} {
			p, err := ping.NewPinger(req.IP)
			if err!=nil { continue }
			p.Count=2; p.Timeout=2*time.Second; p.SetPrivileged(priv)
			if err:=p.Run(); err!=nil {
				if strings.Contains(err.Error(),"permission") || strings.Contains(err.Error(),"operation not permitted") || strings.Contains(err.Error(),"socket") {
					continue
				}
				pingRes["error"]=err.Error(); return
			}
			s:=p.Statistics()
			if s.PacketsRecv==0 { continue }
			pingRes["ok"]=true
			pingRes["latency_ms"]=float64(s.AvgRtt.Microseconds())/1000.0
			pingRes["loss"]=float64(s.PacketLoss)
			return
		}
		// exec ping fallback
		ctx,cancel:=context.WithTimeout(r.Context(),4*time.Second)
		defer cancel()
		if out, ferr:=execPingViaBinary(ctx, req.IP); ferr==nil {
			pingRes["ok"]=out["ok"]
			pingRes["latency_ms"]=out["lat"]
			pingRes["loss"]=out["loss"]
			if out["ok"]==true { return }
		}
		// tcp fallback
		if ok,lat:=tcpReachFallback(req.IP); ok {
			pingRes["ok"]=true; pingRes["latency_ms"]=lat; pingRes["loss"]=0; return
		}
		pingRes["loss"]=100
		if pingRes["error"]==nil { pingRes["error"]="no reply and fallback unreachable" }
	}()
	// snmp
	snmpRes := map[string]any{"ok":false}
	if community!="" && community!="-" {
		func(){
			defer func(){ if r:=recover(); r!=nil { snmpRes["error"]=fmt.Sprintf("%v",r) } }()
			g:=&gosnmp.GoSNMP{Target:req.IP, Port:161, Community:community, Version:gosnmp.Version2c, Timeout:3*time.Second, Retries:1}
			if snmpVer=="1" { g.Version=gosnmp.Version1 }
			if snmpVer=="3" { snmpRes["error"]="SNMPv3 not supported in test"; return }
			if err:=g.Connect(); err!=nil { snmpRes["error"]=err.Error(); return }
			defer g.Conn.Close()
			// try sysDescr
			sysDescr:=""
			if res,err:=g.Get([]string{"1.3.6.1.2.1.1.1.0"}); err==nil && len(res.Variables)>0 {
				switch v:=res.Variables[0].Value.(type){
				case string: sysDescr=v
				case []byte: sysDescr=string(v)
				default: sysDescr=fmt.Sprint(v)
				}
			}
			// walk ifDescr count
			cnt:=0
			_ = g.BulkWalk("1.3.6.1.2.1.2.2.1.2", func(pdu gosnmp.SnmpPDU) error { cnt++; return nil })
			if cnt>0 {
				snmpRes["ok"]=true; snmpRes["interfaces"]=cnt
				if sysDescr!="" { snmpRes["sysDescr"]=sysDescr }
			} else {
				snmpRes["error"]="SNMP ok but no interfaces (timeout or community wrong)"
				if sysDescr!="" { snmpRes["sysDescr"]=sysDescr; snmpRes["ok"]=true; snmpRes["interfaces"]=0 }
			}
		}()
	} else {
		snmpRes["error"]="skipped: no community"
	}
	// ssh dry dial
	sshRes := map[string]any{"ok":false}
	if sshUser!="" {
		func(){
			defer func(){ if r:=recover(); r!=nil { sshRes["error"]=fmt.Sprintf("%v",r) } }()
			addr:=net.JoinHostPort(req.IP, fmt.Sprintf("%d", sshPort))
			cfg:=&ssh.ClientConfig{User:sshUser, Auth:[]ssh.AuthMethod{ssh.Password(sshPass)}, HostKeyCallback:ssh.InsecureIgnoreHostKey(), Timeout:5*time.Second}
			c, err:=ssh.Dial("tcp", addr, cfg)
			if err!=nil { sshRes["error"]=err.Error(); return }
			defer c.Close()
			sshRes["ok"]=true
		}()
	} else {
		sshRes["error"]="skipped: no ssh_user"
	}
	overall:="offline"
	if v,ok:=pingRes["ok"].(bool); ok && v { overall="online" }
	jsonWrite(w, map[string]any{"ip":req.IP, "overall":overall, "ping":pingRes, "snmp":snmpRes, "ssh":sshRes})
}
func execPingViaBinary(ctx context.Context, ip string) (map[string]any, error){
	out, err := exec.CommandContext(ctx, "ping", "-c", "2", "-W", "2", ip).CombinedOutput()
	if err != nil { return map[string]any{"ok":false}, err }
	txt := string(out)
	if !strings.Contains(txt, "bytes from") { return map[string]any{"ok":false}, fmt.Errorf("no reply: %s", strings.TrimSpace(txt)) }
	lat := 1.0; loss := 0
	for _, line := range strings.Split(txt, "\n") {
		if strings.Contains(line, "rtt min/avg/max") || strings.Contains(line, "round-trip min/avg/max") {
			parts := strings.Split(line, "=")
			if len(parts) >= 2 {
				vals := strings.Split(strings.TrimSpace(strings.Split(parts[1], " ")[0]), "/")
				if len(vals) >= 2 { if v, e := strconv.ParseFloat(vals[1], 64); e==nil { lat = v } }
			}
		}
	}
	if strings.Contains(txt, "0% packet loss") { loss = 0 } else if strings.Contains(txt, "100% packet loss") { loss = 100 }
	return map[string]any{"ok":true,"lat":lat,"loss":loss}, nil
}
func tcpReachFallback(ip string) (bool,float64){
	ports:=[]string{"22","80","443","161","8291","8728"}
	for _, pt := range ports {
		st := time.Now()
		c,err:=net.DialTimeout("tcp", net.JoinHostPort(ip,pt), 1200*time.Millisecond)
		if err==nil { c.Close(); return true, float64(time.Since(st).Microseconds())/1000.0 }
	}
	return false,0
}
func execPingFallback(ctx context.Context, ip string) (map[string]any, error){
	if out, err := execPingViaBinary(ctx, ip); err==nil { return out, nil }
	if ok,lat:=tcpReachFallback(ip); ok { return map[string]any{"ok":true,"lat":lat,"loss":0}, nil }
	return map[string]any{"ok":false}, fmt.Errorf("ping and TCP unreachable")
}

func handleListLinks(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"topology"); !ok { return }
	rows,_:= db.Query(r.Context(),`SELECT id,src_id,dst_id,src_type,dst_type,description,capacity,sensor_device_id,sensor_if_index,bps,off FROM links`)
	defer func(){ if rows!=nil{rows.Close()} }()
	var out []map[string]any
	for rows!=nil&&rows.Next(){
		var id,src,dst,st,dt,desc,bps, sdev sql.NullString
		var capv sql.NullInt64; var si sql.NullInt32; var off sql.NullFloat64
		rows.Scan(&id,&src,&dst,&st,&dt,&desc,&capv,&sdev,&si,&bps,&off)
		out=append(out, map[string]any{"id":id.String,"src_id":src.String,"dst_id":dst.String,"src_type":st.String,"dst_type":dt.String,"description":desc.String,"capacity":capv.Int64,"sensor_device_id":sdev.String,"sensor_if_index":func()any{if si.Valid{return si.Int32};return nil}(),"bps":bps.String,"off":func()any{if off.Valid{return off.Float64};return 0}()})
	}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleBootstrap(w http.ResponseWriter, r *http.Request){
	// RBAC: need at least dashboard (same as devices/links cover)
	if _, ok := requireMenu(w, r, "dashboard"); !ok {
		return
	}
	au, _ := getAuthUser(r.Context())
	var (
		devices []map[string]any
		groups  []map[string]any
		racks   []map[string]any
		links   []map[string]any
		errDev, errGrp, errRack, errLink error
		wg sync.WaitGroup
	)
	wg.Add(4)
	go func(){
		defer wg.Done()
		query := `SELECT id,name,ip,type,rack_id,group_id::text,status,x,y,poll_interval,snmp_community,snmp_version,ssh_user,ssh_port,api_user,telnet_port,note FROM devices`
		args := []any{}
		if au != nil && len(au.AllowedGroupIDs) > 0 {
			ph, a := allowedGroupPlaceholders(au)
			query += ` WHERE group_id::text IN `+ph
			args = a
		}
		query += ` ORDER BY name`
		rows, err := db.Query(r.Context(), query, args...)
		if err != nil {
			errDev = err
			return
		}
		defer rows.Close()
		for rows.Next(){
			var id,name,ip,typ,status,comm,sver,sshU,apiU,note string
			var x,y int
			var pi,sp,tp sql.NullInt32
			var rackNull, groupNull, sshUNull, apiUNull, noteNull sql.NullString
			var sverNull, commNull sql.NullString
			if err := rows.Scan(&id,&name,&ip,&typ,&rackNull,&groupNull,&status,&x,&y,&pi,&commNull,&sverNull,&sshUNull,&sp,&apiUNull,&tp,&noteNull); err != nil {
				continue
			}
			rack:=rackNull.String; group:=groupNull.String; comm=commNull.String; sver=sverNull.String; sshU=sshUNull.String; apiU=apiUNull.String; note=noteNull.String
			piv:=any(nil); if pi.Valid{ piv=pi.Int32 }
			spv:=22; if sp.Valid && sp.Int32>0 { spv=int(sp.Int32) }
			tpv:=23; if tp.Valid && tp.Int32>0 { tpv=int(tp.Int32) }
			devices=append(devices, map[string]any{"id":id,"name":name,"ip":ip,"type":typ,"rack_id":rack,"group_id":group,"status":status,"x":x,"y":y,"poll_interval":piv,"snmp_community":comm,"snmp_version":sver,"ssh_user":sshU,"ssh_port":spv,"api_user":apiU,"telnet_port":tpv,"note":note})
		}
		if devices==nil{ devices=[]map[string]any{} }
		_ = errDev
	}()
	go func(){
		defer wg.Done()
		query := "SELECT id::text,name,description,poll_interval FROM device_groups"
		args := []any{}
		if au != nil && len(au.AllowedGroupIDs) > 0 {
			ph, a := allowedGroupPlaceholders(au)
			query += " WHERE id::text IN "+ph
			args = a
		}
		query += " ORDER BY name"
		rows, err := db.Query(r.Context(), query, args...)
		if err != nil {
			errGrp = err
			return
		}
		defer rows.Close()
		for rows.Next(){
			var id,name,desc string; var pi int
			rows.Scan(&id,&name,&desc,&pi)
			groups=append(groups, map[string]any{"id":id,"name":name,"description":desc,"poll_interval":pi})
		}
		if groups==nil{ groups=[]map[string]any{} }
		_ = errGrp
	}()
	go func(){
		defer wg.Done()
		rows, err := db.Query(r.Context(),"SELECT id,name,location,x,y FROM racks ORDER BY name")
		if err != nil {
			errRack = err
			return
		}
		defer rows.Close()
		for rows.Next(){
			var id,name,loc string; var x,y int
			rows.Scan(&id,&name,&loc,&x,&y)
			racks=append(racks, map[string]any{"id":id,"name":name,"location":loc,"x":x,"y":y})
		}
		if racks==nil{ racks=[]map[string]any{} }
		_ = errRack
	}()
	go func(){
		defer wg.Done()
		rows, err := db.Query(r.Context(),`SELECT id,src_id,dst_id,src_type,dst_type,description,capacity,sensor_device_id,sensor_if_index,bps,off FROM links`)
		if err != nil {
			errLink = err
			return
		}
		defer rows.Close()
		for rows.Next(){
			var id,src,dst,st,dt,desc,bps,sdev sql.NullString
			var capv sql.NullInt64; var si sql.NullInt32; var off sql.NullFloat64
			rows.Scan(&id,&src,&dst,&st,&dt,&desc,&capv,&sdev,&si,&bps,&off)
			links=append(links, map[string]any{"id":id.String,"src_id":src.String,"dst_id":dst.String,"src_type":st.String,"dst_type":dt.String,"description":desc.String,"capacity":capv.Int64,"sensor_device_id":sdev.String,"sensor_if_index":func()any{if si.Valid{return si.Int32};return nil}(),"bps":bps.String,"off":func()any{if off.Valid{return off.Float64};return 0}()})
		}
		if links==nil{ links=[]map[string]any{} }
		_ = errLink
	}()
	wg.Wait()
	jsonWrite(w, map[string]any{"devices":devices,"groups":groups,"racks":racks,"links":links,"errors":map[string]any{"devices":func()any{if errDev!=nil{return errDev.Error()};return nil}(),"groups":func()any{if errGrp!=nil{return errGrp.Error()};return nil}(),"racks":func()any{if errRack!=nil{return errRack.Error()};return nil}(),"links":func()any{if errLink!=nil{return errLink.Error()};return nil}()}})
}
func handleCreateLink(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	var req struct{ ID string `json:"id"`; Src string `json:"src_id"`; Dst string `json:"dst_id"`; SrcType string `json:"src_type"`; DstType string `json:"dst_type"`; Desc string `json:"description"`; Cap *int64 `json:"capacity"`; SDev *string `json:"sensor_device_id"`; SIdx *int `json:"sensor_if_index"`; Bps string `json:"bps"`}
	if err:=readJSON(r,&req); err!=nil||req.Src==""||req.Dst==""{ jsonError(w,400,"src_id dst_id required"); return}
	if req.ID==""{ req.ID = fmt.Sprintf("L-%s-%s-%d", req.Src, req.Dst, time.Now().Unix()%100000) }
	if req.SrcType==""{req.SrcType="device"}; if req.DstType==""{req.DstType="device"}
	capv:=int64(1000000000); if req.Cap!=nil{capv=*req.Cap}
	var sdev any; if req.SDev!=nil && *req.SDev!=""{sdev=*req.SDev}
	var sidx any; if req.SIdx!=nil{sidx=*req.SIdx}
	_,err:= db.Exec(r.Context(), `INSERT INTO links(id,src_id,dst_id,src_type,dst_type,description,capacity,sensor_device_id,sensor_if_index,bps) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		req.ID, req.Src, req.Dst, req.SrcType, req.DstType, req.Desc, capv, sdev, sidx, req.Bps)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	jsonWrite(w,map[string]any{"id":req.ID})
}
func handleUpdateLink(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req map[string]any; _=readJSON(r,&req)
	if d,ok:=req["description"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE links SET description=$1 WHERE id=$2", d,id)}
	if b,ok:=req["bps"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE links SET bps=$1 WHERE id=$2", b,id)}
	if c,ok:=req["capacity"].(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE links SET capacity=$1 WHERE id=$2", int64(c),id)}
	if s,ok:=req["sensor_device_id"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE links SET sensor_device_id=$1 WHERE id=$2", s,id)}
	if si,ok:=req["sensor_if_index"].(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE links SET sensor_if_index=$1 WHERE id=$2", int(si),id)}
	jsonWrite(w,map[string]any{"ok":true})
}
func handleDeleteLink(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	_, _= db.Exec(r.Context(),"DELETE FROM links WHERE id=$1", id)
	jsonWrite(w,map[string]any{"ok":true})
}

func handleListInterfaces(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"dashboard"); !ok { return }
	dev:= r.URL.Query().Get("device_id")
	if dev!=""{
		qrows, _ := db.Query(r.Context(),"SELECT id::text,device_id,if_index,name,alias FROM interfaces WHERE device_id=$1 ORDER BY if_index", dev)
		defer func(){ if qrows!=nil{qrows.Close()}}()
		var out []map[string]any
		for qrows!=nil&&qrows.Next(){
			var id, did, name, alias string; var idx int
			qrows.Scan(&id,&did,&idx,&name,&alias)
			out=append(out, map[string]any{"id":id,"device_id":did,"if_index":idx,"name":name,"alias":alias})
		}
		if out==nil{out=[]map[string]any{}}
		jsonWrite(w,out)
		return
	}
	qrows,_:= db.Query(r.Context(),"SELECT id::text,device_id,if_index,name,alias FROM interfaces ORDER BY device_id, if_index LIMIT 500")
	defer func(){ if qrows!=nil{qrows.Close()}}()
	var out []map[string]any
	for qrows!=nil&&qrows.Next(){
		var id,did,name,alias string; var idx int
		qrows.Scan(&id,&did,&idx,&name,&alias)
		out=append(out, map[string]any{"id":id,"device_id":did,"if_index":idx,"name":name,"alias":alias})
	}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateInterface(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	var req struct{ DeviceID string `json:"device_id"`; IfIndex int `json:"if_index"`; Name string `json:"name"`; Alias string `json:"alias"`}
	if err:=readJSON(r,&req); err!=nil||req.DeviceID==""{ jsonError(w,400,"device_id required"); return}
	if req.Name==""{ req.Name = fmt.Sprintf("if%d", req.IfIndex)}
	_,err:= db.Exec(r.Context(),"INSERT INTO interfaces(device_id,if_index,name,alias) VALUES($1,$2,$3,$4) ON CONFLICT(device_id,if_index) DO UPDATE SET name=EXCLUDED.name, alias=EXCLUDED.alias", req.DeviceID, req.IfIndex, req.Name, req.Alias)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	jsonWrite(w,map[string]any{"ok":true})
}

func handleTopology(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"topology"); !ok { return }
	layer:= r.URL.Query().Get("layer")
	if layer==""{layer="top"}
	if layer=="top"{
		racks,_:= db.Query(r.Context(),"SELECT id,name,location,x,y FROM racks ORDER BY name")
		devs,_:= db.Query(r.Context(),"SELECT id,name,ip,type,rack_id FROM devices")
		links,_:= db.Query(r.Context(),"SELECT id,src_id,dst_id,src_type,dst_type,description,bps,off FROM links WHERE src_type='rack' AND dst_type='rack'")
		defer func(){ if racks!=nil{racks.Close()}; if devs!=nil{devs.Close()}; if links!=nil{links.Close()} }()
		var rn []map[string]any; var dn []map[string]any; var ln []map[string]any
		for racks!=nil&&racks.Next(){ var id,name,loc string; var x,y int; racks.Scan(&id,&name,&loc,&x,&y); rn=append(rn, map[string]any{"id":id,"name":name,"location":loc,"x":x,"y":y}) }
		for devs!=nil&&devs.Next(){ var id,name,ip,typ,rack sql.NullString; devs.Scan(&id,&name,&ip,&typ,&rack); dn=append(dn, map[string]any{"id":id.String,"name":name.String,"ip":ip.String,"type":typ.String,"rack_id":rack.String})}
		for links!=nil&&links.Next(){ var id,src,dst,st,dt,desc,bps string; var off sql.NullFloat64; links.Scan(&id,&src,&dst,&st,&dt,&desc,&bps,&off); ln=append(ln, map[string]any{"id":id,"src_id":src,"dst_id":dst,"src_type":st,"dst_type":dt,"description":desc,"bps":bps,"off":func()any{if off.Valid{return off.Float64};return 0}()})}
		jsonWrite(w, map[string]any{"layer":"top","racks":rn,"devices":dn,"links":ln})
		return
	}
	// rack layer
	racks,_:= db.Query(r.Context(),"SELECT id FROM racks WHERE id=$1", layer)
	has:=racks!=nil&&racks.Next(); if racks!=nil{racks.Close()}
	if !has{ jsonError(w,404,"rack not found"); return}
	devRows,_:= db.Query(r.Context(),"SELECT id,name,ip,type,x,y,status FROM devices WHERE rack_id=$1 ORDER BY name", layer)
	linkRows,_:= db.Query(r.Context(),"SELECT id,src_id,dst_id,description,bps,off,sensor_device_id,sensor_if_index FROM links WHERE src_type='device' AND dst_type='device' AND (src_id IN (SELECT id FROM devices WHERE rack_id=$1) OR dst_id IN (SELECT id FROM devices WHERE rack_id=$1))", layer)
	defer func(){ if devRows!=nil{devRows.Close()}; if linkRows!=nil{linkRows.Close()} }()
	var dn []map[string]any; var ln []map[string]any
	for devRows!=nil&&devRows.Next(){ var id,name,ip,typ,status string; var x,y int; devRows.Scan(&id,&name,&ip,&typ,&x,&y,&status); dn=append(dn, map[string]any{"id":id,"name":name,"ip":ip,"type":typ,"x":x,"y":y,"status":status})}
	for linkRows!=nil&&linkRows.Next(){ var id,src,dst,desc,bps,sdev string; var off sql.NullFloat64; var sidx sql.NullInt32; var sdevN sql.NullString
		linkRows.Scan(&id,&src,&dst,&desc,&bps,&off,&sdevN,&sidx)
		sdev=sdevN.String
		ln=append(ln, map[string]any{"id":id,"src_id":src,"dst_id":dst,"description":desc,"bps":bps,"off":func()any{if off.Valid{return off.Float64};return 0}(),"sensor_device_id":sdev,"sensor_if_index":func()any{if sidx.Valid{return sidx.Int32};return nil}()})
	}
	jsonWrite(w, map[string]any{"layer":layer,"devices":dn,"links":ln})
}

func handleListUsers(w http.ResponseWriter, r *http.Request){
	rows,_:= db.Query(r.Context(),`SELECT u.id::text,u.username,u.must_change_password, r.name as role_name FROM users u LEFT JOIN roles r ON r.id=u.role_id ORDER BY u.username`)
	defer func(){ if rows!=nil{rows.Close()} }()
	var out []map[string]any
	for rows!=nil&&rows.Next(){ var id,user,role sql.NullString; var must bool; rows.Scan(&id,&user,&must,&role); out=append(out, map[string]any{"id":id.String,"username":user.String,"must_change_password":must,"role":role.String})}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateUser(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"users"); !ok { return }
	var req struct{ Username string `json:"username"`; Password string `json:"password"`; Role string `json:"role"`; GroupIDs []string `json:"group_ids"`}
	if err:=readJSON(r,&req); err!=nil||req.Username==""||req.Password==""{ jsonError(w,400,"username password required"); return}
	hash,_:= bcrypt.GenerateFromPassword([]byte(req.Password),10)
	var roleID sql.NullString
	if req.Role!=""{
		_ = db.QueryRow(r.Context(),"SELECT id::text FROM roles WHERE name=$1", req.Role).Scan(&roleID)
	}
	var uid string
	err:= db.QueryRow(r.Context(),"INSERT INTO users(username,password_hash,role_id) VALUES($1,$2,$3::uuid) RETURNING id::text", req.Username, string(hash), func()any{if roleID.Valid{return roleID.String};return nil}()).Scan(&uid)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	// role_device_groups via role
	jsonWrite(w, map[string]any{"id":uid})
}
func handleListRoles(w http.ResponseWriter, r *http.Request){
	rows,_:= db.Query(r.Context(),`SELECT r.id::text, r.name, r.mikrotik_group, r.menu_permissions, COALESCE(json_agg(rg.group_id::text) FILTER (WHERE rg.group_id IS NOT NULL),'[]'::json) as groups FROM roles r LEFT JOIN role_device_groups rg ON rg.role_id=r.id GROUP BY r.id ORDER BY r.name`)
	defer func(){ if rows!=nil{rows.Close()} }()
	var out []map[string]any
	for rows!=nil&&rows.Next(){ var id,name,mk string; var perm json.RawMessage; var groups json.RawMessage; rows.Scan(&id,&name,&mk,&perm,&groups); var gids []string; _=json.Unmarshal(groups,&gids); if gids==nil{gids=[]string{}}; out=append(out, map[string]any{"id":id,"name":name,"mikrotik_group":mk,"menu_permissions":perm,"group_ids":gids})}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateRole(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"users"); !ok { return }
	var req struct{ Name string `json:"name"`; MikrotikGroup string `json:"mikrotik_group"`; Menu map[string]bool `json:"menu_permissions"`; GroupIDs []string `json:"group_ids"`}
	if err:=readJSON(r,&req); err!=nil||req.Name==""{ jsonError(w,400,"name required"); return}
	if req.MikrotikGroup==""{req.MikrotikGroup="read"}
	perm,_:= json.Marshal(req.Menu)
	var id string
	err:= db.QueryRow(r.Context(),"INSERT INTO roles(name,mikrotik_group,menu_permissions) VALUES($1,$2,$3) RETURNING id::text", req.Name, req.MikrotikGroup, perm).Scan(&id)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	// link groups
	for _, gid := range req.GroupIDs {
		_, _ = db.Exec(r.Context(),"INSERT INTO role_device_groups(role_id,group_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING", id, gid)
	}
	jsonWrite(w, map[string]any{"id":id})
}

func handleUpdateRole(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"users"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req struct{ Name *string `json:"name"`; MikrotikGroup *string `json:"mikrotik_group"`; Menu map[string]bool `json:"menu_permissions"`; GroupIDs []string `json:"group_ids"`}
	if err:=readJSON(r,&req); err!=nil { jsonError(w,400,"bad json"); return }
	if req.Name!=nil { _,_=db.Exec(r.Context(),"UPDATE roles SET name=$1 WHERE id=$2::uuid", *req.Name, id) }
	if req.MikrotikGroup!=nil { _,_=db.Exec(r.Context(),"UPDATE roles SET mikrotik_group=$1 WHERE id=$2::uuid", *req.MikrotikGroup, id) }
	if req.Menu!=nil { perm,_:=json.Marshal(req.Menu); _,_=db.Exec(r.Context(),"UPDATE roles SET menu_permissions=$1 WHERE id=$2::uuid", perm, id) }
	if req.GroupIDs!=nil {
		_,_=db.Exec(r.Context(),"DELETE FROM role_device_groups WHERE role_id=$1::uuid", id)
		for _,gid:=range req.GroupIDs { _,_=db.Exec(r.Context(),"INSERT INTO role_device_groups(role_id,group_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING", id, gid) }
	}
	jsonWrite(w, map[string]any{"ok":true})
}
func handleDeleteRole(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"users"); !ok { return }
	id:= chi.URLParam(r,"id")
	_,_=db.Exec(r.Context(),"DELETE FROM role_device_groups WHERE role_id=$1::uuid", id)
	_,_=db.Exec(r.Context(),"DELETE FROM roles WHERE id=$1::uuid", id)
	jsonWrite(w, map[string]any{"ok":true})
}
func handleGetRoleGroups(w http.ResponseWriter, r *http.Request){
	id:= chi.URLParam(r,"id")
	rows,_:=db.Query(r.Context(),"SELECT group_id::text FROM role_device_groups WHERE role_id=$1::uuid", id)
	var out []string
	for rows!=nil&&rows.Next(){ var gid string; rows.Scan(&gid); out=append(out,gid) }
	if rows!=nil{rows.Close()}
	if out==nil{out=[]string{}}
	jsonWrite(w, out)
}
func handleUpdateUser(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"users"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req struct{ Username *string `json:"username"`; Password *string `json:"password"`; Role *string `json:"role"`}
	if err:=readJSON(r,&req); err!=nil { jsonError(w,400,"bad json"); return }
	if req.Username!=nil { _,_=db.Exec(r.Context(),"UPDATE users SET username=$1 WHERE id=$2::uuid", *req.Username, id) }
	if req.Password!=nil && *req.Password!="" { hash,_:=bcrypt.GenerateFromPassword([]byte(*req.Password),10); _,_=db.Exec(r.Context(),"UPDATE users SET password_hash=$1 WHERE id=$2::uuid", string(hash), id) }
	if req.Role!=nil {
		if *req.Role=="" { _,_=db.Exec(r.Context(),"UPDATE users SET role_id=NULL WHERE id=$1::uuid", id) } else {
			var rid sql.NullString; _=db.QueryRow(r.Context(),"SELECT id::text FROM roles WHERE name=$1", *req.Role).Scan(&rid)
			if rid.Valid { _,_=db.Exec(r.Context(),"UPDATE users SET role_id=$1::uuid WHERE id=$2::uuid", rid.String, id) }
		}
	}
	// TODO: sync to MikroTik User Manager REST /user-manager/user — stub log
	jsonWrite(w, map[string]any{"ok":true})
}
func handleDeleteUser(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"users"); !ok { return }
	id:= chi.URLParam(r,"id")
	// prevent deleting self? allow but warn — keep simple
	_,_=db.Exec(r.Context(),"DELETE FROM users WHERE id=$1::uuid", id)
	jsonWrite(w, map[string]any{"ok":true})
}
func handleListRoleGroupsDetail(w http.ResponseWriter, r *http.Request){
	rows,_:=db.Query(r.Context(),`SELECT r.id::text, r.name, COALESCE(json_agg(g.name) FILTER (WHERE g.id IS NOT NULL),'[]') FROM roles r LEFT JOIN role_device_groups rg ON rg.role_id=r.id LEFT JOIN device_groups g ON g.id=rg.group_id GROUP BY r.id ORDER BY r.name`)
	var out []map[string]any
	for rows!=nil&&rows.Next(){ var id,name string; var j json.RawMessage; rows.Scan(&id,&name,&j); out=append(out, map[string]any{"id":id,"name":name,"groups":j}) }
	if rows!=nil{rows.Close()}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}

func handleDeviceMetrics(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"dashboard"); !ok { return }
	id:= chi.URLParam(r,"id")
	rows,_:= db.Query(r.Context(),"SELECT time,latency_ms,packet_loss,cpu,mem FROM device_metrics WHERE device_id=$1 ORDER BY time DESC LIMIT 60", id)
	defer func(){ if rows!=nil{rows.Close()}}()
	var out []map[string]any
	for rows!=nil&&rows.Next(){ var t time.Time; var lat,loss,cpu,mem sql.NullFloat64; rows.Scan(&t,&lat,&loss,&cpu,&mem); out=append(out, map[string]any{"time":t,"latency_ms":lat.Float64,"packet_loss":loss.Float64,"cpu":cpu.Float64,"mem":mem.Float64})}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleLinkMetrics(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"topology"); !ok { return }
	id:= chi.URLParam(r,"id")
	rows,_:= db.Query(r.Context(),"SELECT time,in_bps,out_bps FROM link_metrics WHERE link_id=$1 ORDER BY time DESC LIMIT 60", id)
	defer func(){ if rows!=nil{rows.Close()}}()
	var out []map[string]any
	for rows!=nil&&rows.Next(){ var t time.Time; var inb,outb int64; rows.Scan(&t,&inb,&outb); out=append(out, map[string]any{"time":t,"in_bps":inb,"out_bps":outb})}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}



func handleListAlertRules(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	rows,_:= db.Query(r.Context(), `SELECT id::text, tenant_id::text, name, description, enabled, metric, operator, threshold, severity, group_id::text, device_id, cooldown_sec, COALESCE(notify_channel_ids::text,'{}') FROM alert_rules ORDER BY created_at DESC`)
	defer func(){ if rows!=nil{ rows.Close()} }()
	var out []map[string]any
	for rows!=nil && rows.Next(){
		var id, tenant, name, desc, metric, op, sev string
		var enabled bool
		var thr sql.NullFloat64
		var gid, did sql.NullString
		var cd sql.NullInt32
		var notifyRaw sql.NullString
		rows.Scan(&id,&tenant,&name,&desc,&enabled,&metric,&op,&thr,&sev,&gid,&did,&cd,&notifyRaw)
		thrVal:= any(nil); if thr.Valid { thrVal=thr.Float64 }
		cdVal:= 300; if cd.Valid { cdVal=int(cd.Int32) }
		nids:= []string{}
		if notifyRaw.Valid && notifyRaw.String!="" && notifyRaw.String!="{}" {
			s:= strings.Trim(notifyRaw.String,"{}")
			if s!=""{ for _,p:=range strings.Split(s,","){ p=strings.TrimSpace(p); if p!=""{ nids=append(nids,p)}}}
		}
		out=append(out, map[string]any{"id":id,"tenant_id":tenant,"name":name,"description":desc,"enabled":enabled,"metric":metric,"operator":op,"threshold":thrVal,"severity":sev,"group_id":func()any{if gid.Valid{return gid.String};return nil}(),"device_id":func()any{if did.Valid{return did.String};return nil}(),"cooldown_sec":cdVal,"notify_channel_ids":nids})
	}
	if out==nil{ out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateAlertRule(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	var req struct{
		Name string `json:"name"`
		Description string `json:"description"`
		Metric string `json:"metric"`
		Operator string `json:"operator"`
		Threshold *float64 `json:"threshold"`
		Severity string `json:"severity"`
		Enabled *bool `json:"enabled"`
		GroupID *string `json:"group_id"`
		DeviceID *string `json:"device_id"`
		Cooldown *int `json:"cooldown_sec"`
		NotifyIDs []string `json:"notify_channel_ids"`
	}
	if err:=readJSON(r,&req); err!=nil || req.Name=="" || req.Metric=="" { jsonError(w,400,"name and metric required"); return }
	if req.Operator==""{ req.Operator=">"}
	if req.Severity==""{ req.Severity="warning"}
	enabled:=true; if req.Enabled!=nil{ enabled=*req.Enabled}
	cd:=300; if req.Cooldown!=nil && *req.Cooldown>0{ cd=*req.Cooldown}
	var thr any; if req.Threshold!=nil{ thr=*req.Threshold} else { thr=nil}
	var gid any; if req.GroupID!=nil && *req.GroupID!=""{ gid=*req.GroupID} else { gid=nil}
	var did any; if req.DeviceID!=nil && *req.DeviceID!=""{ did=*req.DeviceID} else { did=nil}
	notifyArr:="{}"
	if len(req.NotifyIDs)>0{
		notifyArr="{"+strings.Join(req.NotifyIDs,",")+"}"
	}
	var tenantID string
	_ = db.QueryRow(r.Context(), "SELECT id::text FROM tenants WHERE name='default' LIMIT 1").Scan(&tenantID)
	if tenantID==""{ tenantID="00000000-0000-0000-0000-000000000000"}
	var id string
	err:= db.QueryRow(r.Context(), `INSERT INTO alert_rules(tenant_id, name, description, metric, operator, threshold, severity, enabled, group_id, device_id, cooldown_sec, notify_channel_ids) VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9::uuid,$10,$11,$12::uuid[]) RETURNING id::text`, tenantID, req.Name, req.Description, req.Metric, req.Operator, thr, req.Severity, enabled, gid, did, cd, notifyArr).Scan(&id)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	jsonWrite(w, map[string]any{"id":id})
}
func handleUpdateAlertRule(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req map[string]any
	_ = readJSON(r,&req)
	if v,ok:=req["name"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET name=$1, updated_at=now() WHERE id::text=$2", v, id)}
	if v,ok:=req["description"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET description=$1, updated_at=now() WHERE id::text=$2", v, id)}
	if v,ok:=req["metric"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET metric=$1, updated_at=now() WHERE id::text=$2", v, id)}
	if v,ok:=req["operator"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET operator=$1, updated_at=now() WHERE id::text=$2", v, id)}
	if v,ok:=req["threshold"]; ok{
		if v==nil{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET threshold=NULL, updated_at=now() WHERE id::text=$1", id)
		} else if f,ok:=v.(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET threshold=$1, updated_at=now() WHERE id::text=$2", f, id)}}
	if v,ok:=req["severity"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET severity=$1, updated_at=now() WHERE id::text=$2", v, id)}
	if v,ok:=req["enabled"].(bool); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET enabled=$1, updated_at=now() WHERE id::text=$2", v, id)}
	if v,ok:=req["group_id"]; ok{
		if v==nil || v==""{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET group_id=NULL, updated_at=now() WHERE id::text=$1", id)
		} else if s,ok:=v.(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET group_id=$1::uuid, updated_at=now() WHERE id::text=$2", s, id)}}
	if v,ok:=req["device_id"]; ok{
		if v==nil || v==""{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET device_id=NULL, updated_at=now() WHERE id::text=$1", id)
		} else if s,ok:=v.(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET device_id=$1, updated_at=now() WHERE id::text=$2", s, id)}}
	if v,ok:=req["cooldown_sec"]; ok{
		if f,ok:=v.(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET cooldown_sec=$1, updated_at=now() WHERE id::text=$2", int(f), id)}}
	if v,ok:=req["notify_channel_ids"]; ok{
		if v==nil{ _,_=db.Exec(r.Context(),"UPDATE alert_rules SET notify_channel_ids='{}', updated_at=now() WHERE id::text=$1", id)
		} else if arr,ok:=v.([]any); ok{
			var ids []string; for _,x:=range arr{ if s,ok:=x.(string); ok{ ids=append(ids,s)}}
			narr:="{"+strings.Join(ids,",")+"}"
			_,_=db.Exec(r.Context(),"UPDATE alert_rules SET notify_channel_ids=$1::uuid[], updated_at=now() WHERE id::text=$2", narr, id)
		} else if arr2,ok:=v.([]string); ok{
			_,_=db.Exec(r.Context(),"UPDATE alert_rules SET notify_channel_ids=$1::uuid[], updated_at=now() WHERE id::text=$2", "{"+strings.Join(arr2,",")+"}", id)
		}}
	jsonWrite(w, map[string]any{"ok":true})
}
func handleDeleteAlertRule(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	id:= chi.URLParam(r,"id")
	_,_= db.Exec(r.Context(),"DELETE FROM alert_rules WHERE id::text=$1", id)
	jsonWrite(w, map[string]any{"ok":true})
}
func handleListAlertEvents(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { if _,ok2:=requireMenu(w,r,"dashboard"); !ok2{ return}}
	q:= r.URL.Query()
	status:= q.Get("status")
	severity:= q.Get("severity")
	device:= q.Get("device_id")
	limitS:= q.Get("limit")
	lim:= 100
	if limitS!=""{ if v,err:=strconv.Atoi(limitS); err==nil && v>0 && v<=500{ lim=v}}
	query:= "SELECT e.id::text, e.tenant_id::text, e.rule_id::text, COALESCE(r.name,''), e.device_id, e.group_id::text, e.severity, e.status, e.message, e.metric, e.value, e.threshold, e.fired_at, e.acknowledged_at, e.resolved_at, COALESCE(d.name,''), COALESCE(d.ip,'') FROM alert_events e LEFT JOIN alert_rules r ON r.id=e.rule_id LEFT JOIN devices d ON d.id=e.device_id WHERE 1=1"
	args:= []any{}
	idx:=1
	if status!=""{
		query+= fmt.Sprintf(" AND e.status=$%d", idx); args=append(args,status); idx++
	}
	if severity!=""{
		query+= fmt.Sprintf(" AND e.severity=$%d", idx); args=append(args,severity); idx++
	}
	if device!=""{
		query+= fmt.Sprintf(" AND e.device_id=$%d", idx); args=append(args,device); idx++
	}
	query+= fmt.Sprintf(" ORDER BY e.fired_at DESC LIMIT $%d", idx); args=append(args, lim)
	rows,_:= db.Query(r.Context(), query, args...)
	defer func(){ if rows!=nil{rows.Close()}}()
	var out []map[string]any
	for rows!=nil && rows.Next(){
		var id, tenant, ruleID, ruleName, devID, grp, sev, st, msg, metric, devName, devIP string
		var grpNull sql.NullString
		var val, thr sql.NullFloat64
		var fired, ack, resolved sql.NullTime
		rows.Scan(&id,&tenant,&ruleID,&ruleName,&devID,&grpNull,&sev,&st,&msg,&metric,&val,&thr,&fired,&ack,&resolved,&devName,&devIP)
		grp=""; if grpNull.Valid{ grp=grpNull.String}
		out=append(out, map[string]any{"id":id,"tenant_id":tenant,"rule_id":ruleID,"rule_name":ruleName,"device_id":devID,"group_id":grp,"severity":sev,"status":st,"message":msg,"metric":metric,"value":func()any{if val.Valid{return val.Float64};return nil}(),"threshold":func()any{if thr.Valid{return thr.Float64};return nil}(),"fired_at":fired.Time,"acknowledged_at":func()any{if ack.Valid{return ack.Time};return nil}(),"resolved_at":func()any{if resolved.Valid{return resolved.Time};return nil}(),"device_name":devName,"device_ip":devIP})
	}
	if out==nil{ out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleAckAlert(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	id:= chi.URLParam(r,"id")
	au,_:= getAuthUser(r.Context())
	var uid any; if au!=nil{ uid=au.ID} else { uid=nil}
	_,_= db.Exec(r.Context(), "UPDATE alert_events SET status='acknowledged', acknowledged_at=now(), acknowledged_by=$2::uuid WHERE id::text=$1 AND status='firing'", id, uid)
	jsonWrite(w, map[string]any{"ok":true})
}
func handleResolveAlert(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	id:= chi.URLParam(r,"id")
	_,_= db.Exec(r.Context(), "UPDATE alert_events SET status='resolved', resolved_at=now() WHERE id::text=$1 AND status IN ('firing','acknowledged')", id)
	jsonWrite(w, map[string]any{"ok":true})
}
func handleListAlertChannels(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	rows,_:= db.Query(r.Context(), "SELECT id::text, tenant_id::text, name, type, config::text, enabled, created_at FROM alert_channels ORDER BY created_at DESC")
	defer func(){ if rows!=nil{rows.Close()}}()
	var out []map[string]any
	for rows!=nil && rows.Next(){
		var id, tenant, name, typ, cfg string; var enabled bool; var created time.Time
		rows.Scan(&id,&tenant,&name,&typ,&cfg,&enabled,&created)
		var cfgObj any
		_ = json.Unmarshal([]byte(cfg), &cfgObj)
		if cfgObj==nil{ cfgObj=map[string]any{}}
		out=append(out, map[string]any{"id":id,"tenant_id":tenant,"name":name,"type":typ,"config":cfgObj,"enabled":enabled,"created_at":created})
	}
	if out==nil{ out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateAlertChannel(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	var req struct{ Name string `json:"name"`; Type string `json:"type"`; Config map[string]any `json:"config"`; Enabled *bool `json:"enabled"`}
	if err:=readJSON(r,&req); err!=nil||req.Name==""||req.Type==""{ jsonError(w,400,"name and type required"); return}
	cfgB,_:= json.Marshal(req.Config); if cfgB==nil{ cfgB=[]byte("{}")}
	enabled:= true; if req.Enabled!=nil{ enabled=*req.Enabled}
	var tenantID string
	_ = db.QueryRow(r.Context(), "SELECT id::text FROM tenants WHERE name='default' LIMIT 1").Scan(&tenantID)
	var id string
	err:= db.QueryRow(r.Context(), "INSERT INTO alert_channels(tenant_id, name, type, config, enabled) VALUES($1::uuid,$2,$3,$4,$5) RETURNING id::text", tenantID, req.Name, req.Type, string(cfgB), enabled).Scan(&id)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	jsonWrite(w, map[string]any{"id":id})
}
func handleUpdateAlertChannel(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	id:= chi.URLParam(r,"id")
	var req map[string]any; _=readJSON(r,&req)
	if v,ok:=req["name"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_channels SET name=$1 WHERE id::text=$2", v, id)}
	if v,ok:=req["type"].(string); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_channels SET type=$1 WHERE id::text=$2", v, id)}
	if v,ok:=req["config"]; ok{ b,_:=json.Marshal(v); _,_=db.Exec(r.Context(),"UPDATE alert_channels SET config=$1 WHERE id::text=$2", string(b), id)}
	if v,ok:=req["enabled"].(bool); ok{ _,_=db.Exec(r.Context(),"UPDATE alert_channels SET enabled=$1 WHERE id::text=$2", v, id)}
	jsonWrite(w, map[string]any{"ok":true})
}
func handleDeleteAlertChannel(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { return }
	id:= chi.URLParam(r,"id")
	_,_=db.Exec(r.Context(),"DELETE FROM alert_channels WHERE id::text=$1", id)
	jsonWrite(w, map[string]any{"ok":true})
}
func handleAlertSummary(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"alerts"); !ok { if _,ok2:=requireMenu(w,r,"dashboard"); !ok2{ return}}
	var firing, ack, resolved, crit, warn int
	_ = db.QueryRow(r.Context(),"SELECT COUNT(*) FROM alert_events WHERE status='firing'").Scan(&firing)
	_ = db.QueryRow(r.Context(),"SELECT COUNT(*) FROM alert_events WHERE status='acknowledged'").Scan(&ack)
	_ = db.QueryRow(r.Context(),"SELECT COUNT(*) FROM alert_events WHERE status='resolved'").Scan(&resolved)
	_ = db.QueryRow(r.Context(),"SELECT COUNT(*) FROM alert_events WHERE severity='critical' AND status IN ('firing','acknowledged')").Scan(&crit)
	_ = db.QueryRow(r.Context(),"SELECT COUNT(*) FROM alert_events WHERE severity='warning' AND status IN ('firing','acknowledged')").Scan(&warn)
	jsonWrite(w, map[string]any{"firing":firing,"acknowledged":ack,"resolved":resolved,"critical":crit,"warning":warn})
}


// ========== BASTION ==========

func deviceByID(ctx context.Context, id string) (map[string]any, error){
	var name, ip, typ string
	err := db.QueryRow(ctx, `SELECT name, ip, type FROM devices WHERE id=$1`, id).Scan(&name,&ip,&typ)
	if err!=nil { return nil, err }
	return map[string]any{"id":id,"name":name,"ip":ip,"type":typ}, nil
}
func deviceCreds(ctx context.Context, id string) (ip, sshUser, sshPass string, sshPort int, apiUser, apiPass string, telnetPort int){
	var ipN, suN, spN, auN, apN sql.NullString
	var spI, tpI sql.NullInt32
	_ = db.QueryRow(ctx, `SELECT ip, ssh_user, ssh_pass, ssh_port, api_user, api_pass, telnet_port FROM devices WHERE id=$1`, id).Scan(&ipN,&suN,&spN,&spI,&auN,&apN,&tpI)
	ip = ipN.String; sshUser = suN.String; sshPass = spN.String; apiUser = auN.String; apiPass = apN.String
	sshPort = 22; if spI.Valid && spI.Int32>0 { sshPort = int(spI.Int32) }
	telnetPort = 23; if tpI.Valid && tpI.Int32>0 { telnetPort = int(tpI.Int32) }
	// fallback: use authenticated user's username (from JWT context) for SSH/API user
	if sshUser == "" {
		if au, _ := getAuthUser(ctx); au != nil && au.Username != "" {
			sshUser = au.Username
		} else {
			sshUser = envOr("MIKROTIK_USER", "admin")
		}
	}
	if sshPass == "" { sshPass = envOr("MIKROTIK_PASS", "") }
	if apiUser == "" {
		if au, _ := getAuthUser(ctx); au != nil && au.Username != "" {
			apiUser = au.Username
		} else {
			apiUser = envOr("MIKROTIK_USER", "admin")
		}
	}
	if apiPass == "" { apiPass = envOr("MIKROTIK_PASS", "") }
	return
}

func handleBastionSSH(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	if !websocket.IsWebSocketUpgrade(r) {
		jsonWrite(w, map[string]any{"ok":true,"hint":"Use WebSocket: /api/bastion/ssh?device_id=ID&token=JWT — bastion dials SSH server-side"})
		return
	}
	ws, err := wsUpgrader.Upgrade(w,r,nil)
	if err!=nil { return }
	defer ws.Close()
	did := r.URL.Query().Get("device_id")
	if did=="" { ws.WriteMessage(websocket.TextMessage, []byte("\r\n[ssh] missing device_id\r\n")); return }
	ip, sshUser, sshPass, sshPort, _, _, _ := deviceCreds(r.Context(), did)
	if ip=="" { ws.WriteMessage(websocket.TextMessage, []byte("\r\n[ssh] device not found\r\n")); return }
	// dial ssh
	cfg := &ssh.ClientConfig{User: sshUser, Auth: []ssh.AuthMethod{ssh.Password(sshPass)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 8*time.Second}
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", sshPort))
	ws.WriteMessage(websocket.TextMessage, []byte("\r\n[ssh] connecting to "+addr+" as "+sshUser+" ...\r\n"))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err!=nil { ws.WriteMessage(websocket.TextMessage, []byte("[ssh] dial failed: "+err.Error()+"\r\n")); return }
	defer client.Close()
	sess, err := client.NewSession()
	if err!=nil { ws.WriteMessage(websocket.TextMessage, []byte("[ssh] session failed: "+err.Error()+"\r\n")); return }
	defer sess.Close()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err:= sess.RequestPty("xterm-256color", 32, 120, modes); err!=nil { ws.WriteMessage(websocket.TextMessage, []byte("[ssh] pty failed: "+err.Error()+"\r\n")); return }
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	if err:= sess.Shell(); err!=nil { ws.WriteMessage(websocket.TextMessage, []byte("[ssh] shell failed: "+err.Error()+"\r\n")); return }
	ws.WriteMessage(websocket.TextMessage, []byte("\r\n[ssh] connected — type commands, Ctrl+D to exit\r\n"))
	// ws -> stdin
	go func(){
		for {
			mt, msg, err := ws.ReadMessage()
			if err!=nil { sess.Close(); return }
			if mt==websocket.TextMessage && len(msg)>0 && msg[0]=='{' {
				var ctrl map[string]any
				if err:= json.Unmarshal(msg, &ctrl); err==nil {
					if ctrl["type"]=="resize" {
						if c,ok:=ctrl["cols"].(float64); ok { if r,ok2:=ctrl["rows"].(float64); ok2 { _=sess.WindowChange(int(r), int(c)) }; continue }
					}
				}
			}
			if _,err:= stdin.Write(msg); err!=nil { return }
			if len(msg)==1 && msg[0]==4 { sess.Close(); return }
		}
	}()
	// stdout -> ws
	go func(){
		buf:= make([]byte, 4096)
		for {
			n,err:= stdout.Read(buf)
			if err!=nil { return }
			if n>0 { ws.WriteMessage(websocket.TextMessage, buf[:n]) }
		}
	}()
	go func(){
		buf:= make([]byte, 4096)
		for {
			n,err:= stderr.Read(buf)
			if err!=nil { return }
			if n>0 { ws.WriteMessage(websocket.TextMessage, buf[:n]) }
		}
	}()
	sess.Wait()
	ws.WriteMessage(websocket.TextMessage, []byte("\r\n[ssh] session closed\r\n"))
}

func handleBastionTelnet(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	if !websocket.IsWebSocketUpgrade(r) {
		jsonWrite(w, map[string]any{"ok":true,"hint":"WebSocket: /api/bastion/telnet?device_id=ID&token=JWT"})
		return
	}
	ws, err := wsUpgrader.Upgrade(w,r,nil)
	if err!=nil { return }
	defer ws.Close()
	did := r.URL.Query().Get("device_id")
	if did=="" { ws.WriteMessage(websocket.TextMessage, []byte("missing device_id\r\n")); return }
	ip, _, _, _, _, _, telPort := deviceCreds(r.Context(), did)
	if ip=="" { var tmp string; _=db.QueryRow(r.Context(), `SELECT ip FROM devices WHERE id=$1`, did).Scan(&tmp); ip=tmp }
	if ip=="" { ws.WriteMessage(websocket.TextMessage, []byte("device not found\r\n")); return }
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", telPort))
	ws.WriteMessage(websocket.TextMessage, []byte("[telnet] connecting to "+addr+" ...\r\n"))
	remote, err := net.DialTimeout("tcp", addr, 6*time.Second)
	if err!=nil { ws.WriteMessage(websocket.TextMessage, []byte("[telnet] dial failed: "+err.Error()+"\r\n")); return }
	defer remote.Close()
	ws.WriteMessage(websocket.TextMessage, []byte("[telnet] connected\r\n"))
	go func(){
		for {
			_,msg,err:= ws.ReadMessage()
			if err!=nil { remote.Close(); return }
			remote.Write(msg)
			if len(msg)==1 && msg[0]==4 { return }
		}
	}()
	buf:=make([]byte, 4096)
	for {
		n,err:= remote.Read(buf)
		if err!=nil { ws.WriteMessage(websocket.TextMessage, []byte("\r\n[telnet] closed\r\n")); return }
		if n>0 { ws.WriteMessage(websocket.TextMessage, buf[:n]) }
	}
}

func handleMikrotikAPI(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	did := r.URL.Query().Get("device_id")
	path := r.URL.Query().Get("path")
	if path=="" { path="/rest/interface" }
	var ip string
	if err:= db.QueryRow(r.Context(), `SELECT ip FROM devices WHERE id=$1`, did).Scan(&ip); err!=nil { jsonError(w,404,"device not found"); return }
	_, _, _, _, apiUser, apiPass, _ := deviceCreds(r.Context(), did)
	if apiUser=="" { apiUser = envOr("MIKROTIK_USER","admin") }
	if apiPass=="" { apiPass = envOr("MIKROTIK_PASS","") }
	user, pass := apiUser, apiPass
	target := fmt.Sprintf("http://%s%s", ip, path)
	req, _ := http.NewRequest(r.Method, target, r.Body)
	req.SetBasicAuth(user, pass)
	q := req.URL.Query()
	for k,v := range r.URL.Query(){ if k!="device_id" && k!="path" { q[k]=v } }
	req.URL.RawQuery = q.Encode()
	client := &http.Client{Timeout: 10*time.Second}
	resp, err := client.Do(req)
	if err!=nil { jsonError(w,502, err.Error()); return }
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func handleWebfigProxy(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	did := r.URL.Query().Get("device_id")
	path := strings.TrimPrefix(r.URL.Path, "/api/bastion/webfig")
	if path=="" { path="/" }
	var ip string
	if did!="" {
		_ = db.QueryRow(r.Context(), `SELECT ip FROM devices WHERE id=$1`, did).Scan(&ip)
	}
	if ip=="" { jsonError(w,400,"device_id required"); return }
	target := fmt.Sprintf("http://%s%s", ip, path)
	if r.URL.RawQuery!="" {
		q := r.URL.Query()
		q.Del("device_id")
		if len(q)>0 { target += "?"+q.Encode() }
	}
	req, _ := http.NewRequest("GET", target, nil)
	{
		_, _, _, _, apiUser, apiPass, _ := deviceCreds(r.Context(), did)
		if apiUser=="" { apiUser = envOr("MIKROTIK_USER","") }
		if apiUser!="" { req.SetBasicAuth(apiUser, apiPass) }
	}
	for k,v := range r.Header { if k=="Authorization" || k=="Cookie" { req.Header[k]=v } }
	client := &http.Client{Timeout: 15*time.Second}
	resp, err := client.Do(req)
	if err!=nil { jsonError(w,502, err.Error()); return }
	defer resp.Body.Close()
	for k,v := range resp.Header { w.Header()[k]=v }
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func handleWebConsole(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id := chi.URLParam(r,"id")
	var ip, name string
	_ = db.QueryRow(r.Context(), `SELECT ip, name FROM devices WHERE id=$1`, id).Scan(&ip,&name)
	if ip=="" { jsonError(w,404,"device not found"); return }
	w.Header().Set("Content-Type","text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Web Console — %s (%s)</title><style>html,body{margin:0;height:100%%;background:#0F172A}iframe{width:100%%;height:100%%;border:0}header{background:#0A2463;color:#fff;padding:8px 12px;font:600 .85rem system-ui;display:flex;justify-content:space-between;align-items:center}a{color:#93C5FD;text-decoration:none}</style></head><body><header><span>%s • %s • %s</span><a href="/api/bastion/webfig?device_id=%s" target="_blank">Open raw →</a></header><iframe src="/api/bastion/webfig?device_id=%s"></iframe></body></html>`,
		name, ip, name, id, ip, id, id)
}

var _ = bufio.NewReader
var _ = bytes.NewBuffer
var _ = url.Parse
