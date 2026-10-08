package agent
import("context";"os";"path/filepath";"strings";"testing";"github.com/FyberLabs/hypermesh-host/internal/api")
func TestMaintenancePersistsHoldAndBlocksNewWork(t *testing.T){
 a:=&Agent{cfg:Config{StateDir:t.TempDir()}}
 sha:=strings.Repeat("a",64)
 if err:=a.enterMaintenance(context.Background(),sha);err!=nil{t.Fatal(err)}
 if !a.maintenanceHeld()||!a.Busy()||a.effectiveSell().SellState!=api.SellPaused{t.Fatal("hold not enforced")}
 for _,kind:=range []api.JobKind{api.KindLeaseStart,api.KindLabPull,api.KindPathB,api.KindCompactCert}{
  res:=a.runJob(context.Background(),api.Job{Kind:kind})
  if res.Passed||res.Reason!="host maintenance hold"{t.Fatal("new work admitted")}
 }
 fresh:=&Agent{cfg:a.cfg}
 if !fresh.maintenanceHeld()||fresh.effectiveSell().SellState!=api.SellPaused{t.Fatal("hold lost on restart")}
 if err:=fresh.enterMaintenance(context.Background(),sha);err!=nil{t.Fatal("retry failed",err)}
 if fresh.enterMaintenance(context.Background(),strings.Repeat("b",64))==nil{t.Fatal("other plan replaced hold")}
 if err:=os.Remove(filepath.Join(a.cfg.StateDir,"maintenance.json"));err!=nil{t.Fatal(err)}
 // Without an owner pause the host sells again once the hold is released.
 if fresh.effectiveSell().SellState!=api.SellSelling{t.Fatal("released hold left the host paused")}
}
func TestMalformedMaintenanceHoldsAndCancellationDoNotMutate(t *testing.T){
 a:=&Agent{cfg:Config{StateDir:t.TempDir()}}
 ctx,cancel:=context.WithCancel(context.Background());cancel()
 if a.enterMaintenance(ctx,strings.Repeat("a",64))==nil||a.maintenanceHeld(){t.Fatal("cancelled request mutated")}
 if err:=os.WriteFile(filepath.Join(a.cfg.StateDir,"maintenance.json"),[]byte("invalid"),0600);err!=nil{t.Fatal(err)}
 if !a.maintenanceHeld()||a.effectiveSell().SellState!=api.SellPaused{t.Fatal("invalid marker enabled work")}
 if a.enterMaintenance(context.Background(),strings.Repeat("a",64))==nil{t.Fatal("invalid marker overwritten")}
}
