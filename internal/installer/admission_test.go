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


func TestMinimumUMAOSVisibleMemory(t *testing.T) {
	base := Resources{UMA: true, SystemBytes: 16356589568, AvailableSystemBytes: 14108901376,
		UMARuntime: &UMARuntime{Backend: "cuda", TotalBytes: 16356589568, FreeBytes: 13 * GiB}, DiskFreeBytes: 27 * GiB}
	for _, tc := range []struct {
		name   string
		change func(*Resources)
		pass   bool
	}{
		{"measured NX usable RAM", func(r *Resources) {}, true},
		{"supported NX lower bound", func(r *Resources) { r.SystemBytes = 14 * GiB }, true},
		{"below supported NX lower bound", func(r *Resources) { r.SystemBytes = 14*GiB - 1 }, false},
		{"exactly twelve free OS", func(r *Resources) { r.AvailableSystemBytes = 12 * GiB }, false},
		{"exactly twelve free CUDA", func(r *Resources) { r.UMARuntime.FreeBytes = 12 * GiB }, false},
		{"invalid available exceeds total", func(r *Resources) { r.AvailableSystemBytes = r.SystemBytes + 1 }, false},
		{"unknown runtime", func(r *Resources) { r.UMARuntime = nil }, false},
		{"insufficient model disk", func(r *Resources) { r.DiskFreeBytes = 26 * GiB }, false},
		{"explicit UMA allocation keeps sixteen GiB", func(r *Resources) { r.UMAAllocationBytes = 12 * GiB }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			runtime := *base.UMARuntime
			r.UMARuntime = &runtime
			tc.change(&r)
			if a := CheckResources(r); a.Eligible != tc.pass {
				t.Fatalf("admission %+v", a)
			}
		})
	}
}
