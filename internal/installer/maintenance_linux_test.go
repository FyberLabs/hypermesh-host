//go:build linux
package installer
import("context";"fmt";"strings";"testing";"time")
func TestMaintenanceDrainOrderingAndExplicitRetry(t *testing.T){
 p:=maintenancePlan{SHA256:strings.Repeat("a",64),Operation:"reboot"}
 j:=maintenanceJournal{Schema:1,PlanSHA256:p.SHA256,State:"draining",RecordedAt:time.Now()}
 var order []string
 note:=func(v string)func(context.Context)error{return func(context.Context)error{order=append(order,v);return nil}}
 d:=maintenanceDeps{Hold:note("hold"),Idle:note("idle"),Stop:note("stop"),Stopped:note("verify"),Reboot:note("reboot"),
 Save:func(v maintenanceJournal)error{order=append(order,v.State);return nil}}
 if err:=drainMaintenance(context.Background(),p,&j,d,false);err!=nil{t.Fatal(err)}
 want:=[]string{"hold","idle","stopping","stop","verify","stopped","verify","reboot-requested","verify","reboot"}
 if !sameSetupJSON(order,want){t.Fatal(order)}
 order=nil
 if drainMaintenance(context.Background(),p,&j,d,false)==nil||len(order)!=0{t.Fatal("automatic reboot retry")}
 if err:=drainMaintenance(context.Background(),p,&j,d,true);err!=nil{t.Fatal(err)}
 if strings.Contains(strings.Join(order,","),"hold"){t.Fatal("retry restarted drain")}
}
func TestMaintenanceFailuresNeverSkipDrainOrDurableRebootIntent(t *testing.T){
 for _,stage:=range []string{"hold","idle","stop","verify","save","reboot"}{t.Run(stage,func(t *testing.T){
  p:=maintenancePlan{SHA256:strings.Repeat("a",64),Operation:"reboot"}
  j:=maintenanceJournal{Schema:1,PlanSHA256:p.SHA256,State:"draining",RecordedAt:time.Now()}
  rebooted:=false
  probe:=func(name string)func(context.Context)error{return func(context.Context)error{if name==stage{return fmt.Errorf("failure")};return nil}}
  d:=maintenanceDeps{Hold:probe("hold"),Idle:probe("idle"),Stop:probe("stop"),Stopped:probe("verify"),Save:func(maintenanceJournal)error{if stage=="save"{return fmt.Errorf("disk full")};return nil},Reboot:func(context.Context)error{rebooted=true;return fmt.Errorf("lost reply")}}
  if drainMaintenance(context.Background(),p,&j,d,false)==nil{t.Fatal("failure accepted")}
  if rebooted!=(stage=="reboot"){t.Fatal("reboot before safe checkpoint")}
 })}
}

func TestMaintenanceRecoversInterruptedStopWithoutOwnerSocket(t *testing.T){
 p:=maintenancePlan{SHA256:strings.Repeat("a",64),Operation:"stop"}
 j:=maintenanceJournal{Schema:1,PlanSHA256:p.SHA256,State:"stopping",RecordedAt:time.Now()}
 stopped:=0
 d:=maintenanceDeps{Hold:func(context.Context)error{t.Fatal("lost owner socket must not be required");return nil},Idle:func(context.Context)error{t.Fatal("already drained");return nil},
 Stop:func(context.Context)error{stopped++;return nil},Stopped:func(context.Context)error{return nil},Save:func(maintenanceJournal)error{return nil}}
 if err:=drainMaintenance(context.Background(),p,&j,d,false);err!=nil||stopped!=1||j.State!="stopped"{t.Fatal(err,j.State)}
}

func TestMaintenanceHoldReleasedOnlyAfterResumeCheckpoint(t *testing.T){
 hold:=fmt.Errorf("matching maintenance pause required")
 if released,err:=maintenanceHoldReleased(nil,false,"stopped");released||err!=nil{t.Fatal("present hold treated as released")}
 for _,state:=range []string{"resuming","complete"}{
  if released,err:=maintenanceHoldReleased(hold,true,state);!released||err!=nil{t.Fatal("released hold blocks retry",state)}
 }
 if _,err:=maintenanceHoldReleased(hold,true,"stopped");err==nil{t.Fatal("missing hold accepted before resume began")}
 if _,err:=maintenanceHoldReleased(hold,false,"resuming");err==nil{t.Fatal("mismatched hold accepted")}
}
