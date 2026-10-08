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
 // sellMu stays held until the marker exists, so a posture read in between
 // cannot settle the hold's pause back to the kept posture. If the marker
 // write fails, the next read restores the kept posture.
 a.sellMu.Lock()
 defer a.sellMu.Unlock()
 if err:=a.holdSellForMaintenance();err!=nil{return fmt.Errorf("cannot persist indefinite sell pause")}
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

// holdSellForMaintenance writes the hold's indefinite pause to sell.json in
// the shape earlier installers expect. The posture to restore afterwards is
// kept beside it first: the owner's pause, or selling when there is none.
// Caller must hold sellMu.
func (a *Agent) holdSellForMaintenance()error{
 if !a.maintenanceHeld(){
  raw,_:=os.ReadFile(a.sellPath())
  kept:=SellPosture{SellState:api.SellSelling}
  if owner,ok:=ownerPauseIntent(a.loadSell(),raw);ok&&!untilReached(owner.Until,a.clock()){kept=owner}
  if err:=writeJSON0600(a.sellOwnerPath(),kept);err!=nil{return err}
 }
 return a.saveSell(SellPosture{SellState:api.SellPaused})
}
