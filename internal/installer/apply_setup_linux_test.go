//go:build linux

package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupStateDir(t *testing.T)string {
	t.Helper()
	dir,err:=os.MkdirTemp("/tmp","hypermesh-setup-test-")
	if err!=nil{t.Fatal(err)}
	t.Cleanup(func(){_ = os.RemoveAll(dir)})
	return dir
}

func TestSetupJournalPermissionsLockAndStatus(t *testing.T) {
	dir:=setupStateDir(t)
	unlock,err:=lockSetupState(dir);if err!=nil{t.Fatal(err)}
	if second,err:=lockSetupState(dir);err==nil{second();t.Fatal("concurrent apply accepted")}
	unlock()
	unlock,err=lockSetupState(dir);if err!=nil{t.Fatal("lock did not release",err)}
	defer unlock()
	digest:=strings.Repeat("a",64)
	j:=setupJournal{Schema:1,PlanSHA256:digest,Stage:"packages",State:"running",OriginalTarget:"graphical.target"}
	path:=filepath.Join(dir,digest+".json")
	if err:=writeSetupJournal(path,j);err!=nil{t.Fatal(err)}
	info,err:=os.Stat(path);if err!=nil || info.Mode().Perm()!=0o600{t.Fatal("journal is not private",err)}
	raw,err:=readSetupRegular(path,true);if err!=nil{t.Fatal(err)}
	var got setupJournal
	if err:=decodeSetupJSON(raw,&got);err!=nil || got!=j{t.Fatal(got,err)}
	j.Stage="prerequisites";j.State="complete"
	if err:=writeSetupJournal(path,j);err!=nil{t.Fatal(err)}
	var out,diagnostic bytes.Buffer
	code:=RunSetupStatus(context.Background(),[]string{"--state-dir",dir,"--plan-sha",digest},&out,&diagnostic)
	if code!=0 || json.Unmarshal(out.Bytes(),&got)!=nil || got!=j {t.Fatalf("%d %s %s",code,&out,&diagnostic)}
	entries,_:=os.ReadDir(dir)
	for _,entry:=range entries{if strings.HasPrefix(entry.Name(),".setup-journal-"){t.Fatal("staging file leaked")}}
}

func TestSetupStateRejectsLinksAndLoosePermissions(t *testing.T) {
	base:=setupStateDir(t)
	target:=filepath.Join(base,"real")
	if err:=os.Mkdir(target,0o700);err!=nil{t.Fatal(err)}
	link:=filepath.Join(base,"link")
	if err:=os.Symlink(target,link);err!=nil{t.Fatal(err)}
	if _,err:=lockSetupState(link);err==nil{t.Fatal("symlinked state directory accepted")}
	if err:=os.Chmod(target,0o755);err!=nil{t.Fatal(err)}
	if _,err:=lockSetupState(target);err==nil{t.Fatal("public state directory accepted")}
	if err:=os.Chmod(target,0o700);err!=nil{t.Fatal(err)}
	other:=filepath.Join(base,"other")
	if err:=os.WriteFile(other,[]byte("preserve"),0o600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(other,filepath.Join(target,"apply.lock"));err!=nil{t.Fatal(err)}
	if _,err:=lockSetupState(target);err==nil{t.Fatal("symlinked lock accepted")}
	if raw,_:=os.ReadFile(other);string(raw)!="preserve"{t.Fatal("lock changed link target")}
	if _,err:=readSetupRegular(filepath.Join(target,"apply.lock"),false);err==nil{t.Fatal("symlinked input accepted")}
	if err:=os.Chmod(other,0o644);err!=nil{t.Fatal(err)}
	if _,err:=readSetupRegular(other,true);err==nil{t.Fatal("public journal accepted")}
}

func TestSetupStatusDoesNotCreateMissingState(t *testing.T) {
	base:=setupStateDir(t)
	dir:=filepath.Join(base,"missing")
	var out bytes.Buffer
	code:=RunSetupStatus(context.Background(),[]string{"--state-dir",dir,"--plan-sha",strings.Repeat("a",64)},&out,&out)
	if code!=1{t.Fatal(code)}
	if _,err:=os.Stat(dir);!os.IsNotExist(err){t.Fatal("status created state")}
}

func TestApplyCLIRejectsApprovalBeforeStateCreation(t *testing.T) {
	f:=newApplyFixture(t,false,false)
	dir:=setupStateDir(t)
	planPath:=filepath.Join(dir,"plan.json")
	raw,_:=json.Marshal(f.plan)
	if err:=os.WriteFile(planPath,raw,0o600);err!=nil{t.Fatal(err)}
	state:=filepath.Join(dir,"state")
	var out bytes.Buffer
	code:=RunApplySetup(context.Background(),[]string{"--plan",planPath,"--approve-plan",strings.Repeat("0",64),"--state-dir",state},&out,&out)
	if code!=1{t.Fatal("invalid approval accepted")}
	if _,err:=os.Stat(state);!os.IsNotExist(err){t.Fatal("invalid approval wrote state")}
}
