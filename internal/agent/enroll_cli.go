package agent

import (
	"context"
	"flag"
	"fmt"
	"os"
)

// RunEnrollCLI enrolls once and exits. --yes is accepted as a flag so it
// cannot be mistaken for Terms acceptance; it does not accept the Terms.
func RunEnrollCLI(ctx context.Context, cfg Config, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	license := fs.String("license", "", "accept the Hypermesh Terms; the only value is accept")
	fs.Bool("yes", false, "ignored; does not accept the Hypermesh Terms")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("hypermesh: enroll does not take arguments")
	}
	if err := ApplyLicenseFlag(&cfg, *license); err != nil {
		return err
	}
	a, err := New(cfg, nil)
	if err != nil {
		return err
	}
	if err := a.ensureEnrolled(ctx); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "enrolled %s\n", a.state.DeviceID)
	return nil
}
