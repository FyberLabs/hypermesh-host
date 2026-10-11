package hostcli

import (
	"reflect"
	"testing"
)

func TestGuidedInstallArgs(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		guided bool
		want   []string
	}{
		{nil, true, []string{}},
		{[]string{"install"}, true, []string{}},
		{[]string{"install", "--yes"}, true, []string{"--yes"}},
		{[]string{"--yes"}, true, []string{"--yes"}},
		{[]string{"--yes", "--network", "test"}, true, []string{"--yes", "--network", "test"}},
		{[]string{"--cache-settings"}, true, []string{"--cache-settings"}},
		{[]string{"-h"}, false, nil},
		{[]string{"--help"}, false, nil},
		{[]string{"help"}, false, nil},
		{[]string{"sell", "pause"}, false, nil},
		{[]string{"advanced-help"}, false, nil},
	} {
		guided, args := guidedInstallArgs(tc.args)
		if guided != tc.guided || !reflect.DeepEqual(args, tc.want) {
			t.Fatalf("%v: guided=%v args=%v", tc.args, guided, args)
		}
	}
}
