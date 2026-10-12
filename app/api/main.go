package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

var (
	db  *pgxpool.Pool
	rdb *redis.Client
	jwtKey []byte
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
		pr.Get("/api/roles", handleListRoles)
		pr.Post("/api/roles", handleCreateRole)
		// metrics
		pr.Get("/api/metrics/device/{id}", handleDeviceMetrics)
		pr.Get("/api/metrics/link/{id}", handleLinkMetrics)
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
	query := `SELECT id,name,ip,type,rack_id,group_id::text,status,x,y,poll_interval,snmp_community FROM devices`
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
		var id,name,ip,typ,rack,group,status,comm string
		var x,y int
		var pi sql.NullInt32
		var rackNull, groupNull sql.NullString
		rows.Scan(&id,&name,&ip,&typ,&rackNull,&groupNull,&status,&x,&y,&pi,&comm)
		rack=rackNull.String; group=groupNull.String
		piv:=any(nil); if pi.Valid{ piv=pi.Int32 }
		out=append(out, map[string]any{"id":id,"name":name,"ip":ip,"type":typ,"rack_id":rack,"group_id":group,"status":status,"x":x,"y":y,"poll_interval":piv,"snmp_community":comm})
	}
	if out==nil{out=[]map[string]any{}}
	jsonWrite(w,out)
}
func handleCreateDevice(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	var req struct{ ID string `json:"id"`; Name string `json:"name"`; IP string `json:"ip"`; Type string `json:"type"`; RackID *string `json:"rack_id"`; GroupID *string `json:"group_id"`; PollInterval *int `json:"poll_interval"`; X *int `json:"x"`; Y *int `json:"y"`}
	if err:=readJSON(r,&req); err!=nil||req.Name==""||req.IP==""{ jsonError(w,400,"name and ip required"); return}
	if req.ID==""{ req.ID = strings.ToUpper(strings.ReplaceAll(req.Name," ","-")) + fmt.Sprintf("-%d", time.Now().Unix()%10000) }
	if req.Type==""{ req.Type="mikrotik" }
	x,y:=100,100; if req.X!=nil{x=*req.X}; if req.Y!=nil{y=*req.Y}
	var gid any; if req.GroupID!=nil && *req.GroupID!=""{gid=*req.GroupID} else { gid=nil }
	var rid any; if req.RackID!=nil && *req.RackID!=""{rid=*req.RackID} else { rid=nil }
	var pi any; if req.PollInterval!=nil{pi=*req.PollInterval} else {pi=nil}
	_,err:= db.Exec(r.Context(), `INSERT INTO devices(id,name,ip,type,rack_id,group_id,poll_interval,x,y) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		req.ID, req.Name, req.IP, req.Type, rid, gid, pi, x,y)
	if err!=nil{ jsonError(w,500,err.Error()); return}
	jsonWrite(w, map[string]any{"id":req.ID})
}
func handleGetDevice(w http.ResponseWriter, r *http.Request){
	id:= chi.URLParam(r,"id")
	var name,ip,typ,status string; var x,y int
	err:= db.QueryRow(r.Context(),"SELECT name,ip,type,status,x,y FROM devices WHERE id=$1", id).Scan(&name,&ip,&typ,&status,&x,&y)
	if err!=nil{ jsonError(w,404,"not found"); return}
	jsonWrite(w, map[string]any{"id":id,"name":name,"ip":ip,"type":typ,"status":status,"x":x,"y":y})
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
	if pi,ok:=req["poll_interval"].(float64); ok{ _,_=db.Exec(r.Context(),"UPDATE devices SET poll_interval=$1 WHERE id=$2", int(pi),id)}
	jsonWrite(w,map[string]any{"ok":true})
}
func handleDeleteDevice(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	id:= chi.URLParam(r,"id")
	_, _= db.Exec(r.Context(),"DELETE FROM devices WHERE id=$1", id)
	jsonWrite(w,map[string]any{"ok":true})
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
	rows,_:= db.Query(r.Context(),"SELECT id::text,name,mikrotik_group,menu_permissions FROM roles ORDER BY name")
	defer func(){ if rows!=nil{rows.Close()} }()
	var out []map[string]any
	for rows!=nil&&rows.Next(){ var id,name,mk string; var perm json.RawMessage; rows.Scan(&id,&name,&mk,&perm); out=append(out, map[string]any{"id":id,"name":name,"mikrotik_group":mk,"menu_permissions":perm})}
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


// ========== BASTION ==========

func deviceByID(ctx context.Context, id string) (map[string]any, error){
	var name, ip, typ string
	err := db.QueryRow(ctx, `SELECT name, ip, type FROM devices WHERE id=$1`, id).Scan(&name,&ip,&typ)
	if err!=nil { return nil, err }
	return map[string]any{"id":id,"name":name,"ip":ip,"type":typ}, nil
}

func handleBastionSSH(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	if r.Header.Get("Upgrade")=="" {
		jsonWrite(w, map[string]any{"ok":true,"hint":"Use WebSocket to this endpoint with ?device_id=ID. SSH via bastion requires device credentials."})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok { jsonError(w,500,"hijack not supported"); return }
	conn, bw, err := hj.Hijack()
	if err!=nil { return }
	defer conn.Close()
	bw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: stub\r\n\r\n")
	bw.Flush()
	io.WriteString(conn, "\r\n[NMS Bastion SSH] device_id="+r.URL.Query().Get("device_id")+" — SSH bridge pending x/crypto/ssh (v0.6).\r\nUse: ssh admin@<device_ip> via credential or Web Console.\r\n")
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err!=nil { return }
		conn.Write(buf[:n])
	}
}

func handleBastionTelnet(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	if r.Header.Get("Upgrade")=="" {
		jsonWrite(w, map[string]any{"ok":true,"hint":"WebSocket to /api/bastion/telnet?device_id=ID"})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok { jsonError(w,500,"hijack not supported"); return }
	conn, bw, err := hj.Hijack()
	if err!=nil { return }
	defer conn.Close()
	bw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: stub\r\n\r\n")
	bw.Flush()
	did := r.URL.Query().Get("device_id")
	var ip string
	_ = db.QueryRow(r.Context(), `SELECT ip FROM devices WHERE id=$1`, did).Scan(&ip)
	if ip=="" { ip="127.0.0.1" }
	remote, err := net.DialTimeout("tcp", net.JoinHostPort(ip,"23"), 5*time.Second)
	if err!=nil {
		io.WriteString(conn, "[bastion] telnet dial failed: "+err.Error()+"\r\n")
		return
	}
	defer remote.Close()
	go io.Copy(remote, conn)
	io.Copy(conn, remote)
}

func handleMikrotikAPI(w http.ResponseWriter, r *http.Request){
	if _,ok:=requireMenu(w,r,"devices"); !ok { return }
	did := r.URL.Query().Get("device_id")
	path := r.URL.Query().Get("path")
	if path=="" { path="/rest/interface" }
	var ip string
	if err:= db.QueryRow(r.Context(), `SELECT ip FROM devices WHERE id=$1`, did).Scan(&ip); err!=nil { jsonError(w,404,"device not found"); return }
	user := envOr("MIKROTIK_USER","admin")
	pass := envOr("MIKROTIK_PASS","")
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
	if u := envOr("MIKROTIK_USER",""); u!="" {
		req.SetBasicAuth(u, envOr("MIKROTIK_PASS",""))
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
