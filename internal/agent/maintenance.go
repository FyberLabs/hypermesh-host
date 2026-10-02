package agent

import (
 "context"
 "encoding/json"
 "fmt"
 "net/http"
 "os"
 "path/filepath"
 "time"

 "github.com/FyberLabs/hypermesh-host/internal/api"
)

// Any marker, including an unreadable one, holds new work and self-updates.
// Only the reviewed installer resume operation removes this durable hold.
func (a *Agent) maintenanceHeld()bool{
 _,err:=os.Lstat(filepath.Join(a.cfg.StateDir,"maintenance.json"))
 return !os.IsNotExist(err)
}
func (a *Agent) enterMaintenance(ctx context.Context,plan string)error{
 if len(plan)!=64{return fmt.Errorf("maintenance plan digest required")}
 for _,c:=range plan{if !(c>='0'&&c<='9'||c>='a'&&c<='f'){return fmt.Errorf("invalid maintenance plan digest")}}
 a.maintenanceMu.Lock();defer a.maintenanceMu.Unlock()
 if err:=ctx.Err();err!=nil{return err}
 path:=filepath.Join(a.cfg.StateDir,"maintenance.json")
 if raw,err:=os.ReadFile(path);err==nil{
  var old struct{PlanSHA256 string `json:"plan_sha256"`}
  if json.Unmarshal(raw,&old)!=nil||old.PlanSHA256!=plan{return fmt.Errorf("another maintenance plan is already recorded")}
 }else if !os.IsNotExist(err){return fmt.Errorf("maintenance state unavailable")}
 a.sellMu.Lock()
 err:=a.saveSell(SellPosture{SellState:api.SellPaused})
 a.sellMu.Unlock()
 if err!=nil{return fmt.Errorf("cannot persist indefinite sell pause")}
 // Atomic private writer syncs before publishing. Hold persists across reboot.
 return writeJSON0600(path,map[string]any{"schema":1,"plan_sha256":plan,"requested_at":time.Now().UTC()})
}
func (a *Agent) handleOwnerMaintenance(w http.ResponseWriter,r *http.Request){
 if r.Method!=http.MethodPost{w.WriteHeader(http.StatusMethodNotAllowed);return}
 var req struct{PlanSHA256 string `json:"plan_sha256"`}
 dec:=json.NewDecoder(http.MaxBytesReader(w,r.Body,4096));dec.DisallowUnknownFields()
 if dec.Decode(&req)!=nil{http.Error(w,"invalid maintenance request",http.StatusBadRequest);return}
 if a.enterMaintenance(r.Context(),req.PlanSHA256)!=nil{http.Error(w,"maintenance request not confirmed; inspect local state",http.StatusConflict);return}
 encodeJSON(w,http.StatusOK,map[string]any{"maintenance":true,"selling_allowed":false})
}
