package image

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/tart"
)

// goldenName matches only the full names Build gives golden images, so prune
// never touches base images, build scratch VMs, job clones or user VMs.
var goldenName = regexp.MustCompile(`^offscreen-[0-9]{8}-xcode[0-9]+(?:\.[0-9]+)*-[0-9a-f]{12}$`)

// PruneEntry is one golden image prune selected, skipped or failed to delete.
type PruneEntry struct {
	Name   string
	Reason string
}

// PruneReport describes one prune pass. Eligible lists what a dry run would
// delete; Deleted and Failed are filled only when Apply is set.
type PruneReport struct {
	Current  string
	Apply    bool
	Eligible []PruneEntry
	Deleted  []PruneEntry
	Skipped  []PruneEntry
	Failed   []PruneEntry
}

type vmClient interface {
	List(ctx context.Context) ([]tart.VM, error)
	Delete(ctx context.Context, name string) error
}

// Prune previews or deletes golden images other than the configured
// current_image. Running images are skipped. A non-nil confirmed limits
// deletion to the images a user agreed to delete after a preview; any other
// image, such as the previous current_image after a build published during
// the prompt, is skipped. When apply is set, guard runs
// under the image lock before anything is read, so no image build can publish
// between the caller's safety check and the deletes; a guard error deletes
// nothing.
func Prune(ctx context.Context, path string, apply bool, confirmed []string, guard func(context.Context) error) (PruneReport, error) {
	unlock, err := lockImages(path)
	if err != nil {
		return PruneReport{}, err
	}
	defer unlock()
	if apply && guard != nil {
		if err := guard(ctx); err != nil {
			return PruneReport{}, err
		}
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		return PruneReport{}, err
	}
	return prune(ctx, tart.Client{}, cfg.CurrentImage, apply, confirmed)
}

func prune(ctx context.Context, client vmClient, current string, apply bool, confirmed []string) (PruneReport, error) {
	report := PruneReport{Current: current, Apply: apply}
	if current == "" {
		return report, errors.New("no current_image is set; run offscreen image build before pruning images")
	}
	vms, err := client.List(ctx)
	if err != nil {
		return report, err
	}
	// A stale or mistyped current_image would make every golden image look old.
	if !slices.ContainsFunc(vms, func(vm tart.VM) bool { return vm.Name == current }) {
		return report, fmt.Errorf("current_image %q is not in tart list; refusing to prune (check OFFSCREEN_CONFIG or run offscreen image build)", current)
	}
	for _, vm := range vms {
		if vm.Name == current || !goldenName.MatchString(vm.Name) {
			continue
		}
		entry := PruneEntry{Name: vm.Name}
		if vm.Running {
			entry.Reason = "running"
			report.Skipped = append(report.Skipped, entry)
			continue
		}
		if confirmed != nil && !slices.Contains(confirmed, vm.Name) {
			entry.Reason = "not in the confirmed selection"
			report.Skipped = append(report.Skipped, entry)
			continue
		}
		report.Eligible = append(report.Eligible, entry)
		if !apply {
			continue
		}
		if err := client.Delete(ctx, vm.Name); err != nil {
			entry.Reason = err.Error()
			report.Failed = append(report.Failed, entry)
			continue
		}
		report.Deleted = append(report.Deleted, entry)
	}
	return report, nil
}
