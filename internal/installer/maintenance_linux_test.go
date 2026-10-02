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
 want:=[]string{"hold","idle","stop","verify","stopped","verify","reboot-requested","verify","reboot"}
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
