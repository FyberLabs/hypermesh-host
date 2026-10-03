package installer

import "testing"

func TestResourceAdmission(t *testing.T) {
	tests := []struct {
		name string
		r Resources
		want bool
	}{
		{"unknown", Resources{}, false},
		{"minimum discrete", Resources{SystemBytes: 16*GiB, DiscreteVRAMBytes: 12*GiB, DiskFreeBytes: 27*GiB}, true},
		{"insufficient RAM", Resources{SystemBytes: 15*GiB, DiscreteVRAMBytes: 12*GiB, DiskFreeBytes: 27*GiB}, false},
		{"insufficient VRAM", Resources{SystemBytes: 16*GiB, DiscreteVRAMBytes: 11*GiB, DiskFreeBytes: 27*GiB}, false},
		{"AGX proposed allocation", Resources{SystemBytes: 64*GiB, AvailableSystemBytes: 48*GiB, UMA: true, UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 100*GiB, TotalBytes: 128*GiB}, UMAAllocationBytes: 40*GiB, DiskFreeBytes: 88*GiB}, true},
		{"N5 proposed allocation", Resources{SystemBytes: 128*GiB, AvailableSystemBytes: 100*GiB, UMA: true, UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 100*GiB, TotalBytes: 128*GiB}, UMAAllocationBytes: 80*GiB, DiskFreeBytes: 176*GiB}, true},
		{"UMA uses available not installed", Resources{SystemBytes: 128*GiB, AvailableSystemBytes: 10*GiB, UMA: true, UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 100*GiB, TotalBytes: 128*GiB}, UMAAllocationBytes: 12*GiB, DiskFreeBytes: 100*GiB}, false},
		{"UMA minimum without fixed allocation", Resources{SystemBytes: 128*GiB, AvailableSystemBytes: 100*GiB, UMA: true, UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 100*GiB, TotalBytes: 128*GiB}, DiskFreeBytes: 100*GiB}, true},
		{"UMA cannot count discrete VRAM", Resources{SystemBytes: 128*GiB, AvailableSystemBytes: 100*GiB, DiscreteVRAMBytes: 24*GiB, UMA: true, UMARuntime: &UMARuntime{Backend: "cuda", FreeBytes: 11*GiB, TotalBytes: 128*GiB}, DiskFreeBytes: 100*GiB}, false},
		{"wrong mode", Resources{SystemBytes: 64*GiB, DiscreteVRAMBytes: 24*GiB, UMAAllocationBytes: 12*GiB, DiskFreeBytes: 100*GiB}, false},
		{"invalid free RAM", Resources{SystemBytes: 16*GiB, AvailableSystemBytes: 17*GiB, DiscreteVRAMBytes: 12*GiB, DiskFreeBytes: 100*GiB}, false},
		{"overflow", Resources{SystemBytes: 16*GiB, DiscreteVRAMBytes: 1<<63-1, DiskFreeBytes: 1<<63-1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckResources(tt.r)
			if got.Eligible != tt.want {
				t.Fatalf("eligible=%v want=%v: %v", got.Eligible, tt.want, got.Reasons)
			}
			if !got.Eligible && len(got.Reasons) == 0 {
				t.Fatal("rejection must explain remediation")
			}
		})
	}
}

func TestDiskBoundaryRoundsUp(t *testing.T) {
	r := Resources{SystemBytes: 16*GiB, DiscreteVRAMBytes: 12*GiB+1}
	want := (r.DiscreteVRAMBytes*11 + 4)/5
	r.DiskFreeBytes = want
	if got := CheckResources(r); !got.Eligible || got.RequiredDiskBytes != want {
		t.Fatalf("exact boundary failed: %+v", got)
	}
	r.DiskFreeBytes--
	if CheckResources(r).Eligible {
		t.Fatal("one byte below required free disk must fail")
	}
}

func TestInstallationMinimumIsNotAWorkloadBudget(t *testing.T) {
 for _,free:=range []int64{12*GiB-1,12*GiB,12*GiB+1,48*GiB} {
  r:=Resources{SystemBytes:64*GiB,AvailableSystemBytes:free,UMA:true,
   UMARuntime:&UMARuntime{Backend:"cuda",FreeBytes:free,TotalBytes:64*GiB},DiskFreeBytes:27*GiB}
  a:=CheckResources(r)
  if a.Eligible!=(free>12*GiB) {t.Fatalf("free=%d: %+v",free,a)}
  if a.AcceleratorBytes!=0||a.MaximumUMABytes!=0 {t.Fatal("installation recorded a workload budget",a)}
 }
 for _,kind:=range []string{"system","runtime","missing-runtime","disk"} {
  r:=Resources{SystemBytes:64*GiB,AvailableSystemBytes:48*GiB,UMA:true,
   UMARuntime:&UMARuntime{Backend:"cuda",FreeBytes:48*GiB,TotalBytes:64*GiB},DiskFreeBytes:27*GiB}
  switch kind {case "system":r.AvailableSystemBytes=12*GiB;case "runtime":r.UMARuntime.FreeBytes=12*GiB;case "missing-runtime":r.UMARuntime=nil;case "disk":r.DiskFreeBytes=1}
  if CheckResources(r).Eligible {t.Fatal("invalid minimum accepted",kind)}
 }
}
