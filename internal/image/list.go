package image

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/tart"
)

// Golden is one golden image in Tart's inventory.
type Golden struct {
	Name    string
	Current bool
	Running bool
	// Created is the build date from the image name; zero when the name has
	// none, as for a current_image set by hand.
	Created time.Time
	// SizeGB is the disk space the image uses on the host, in gigabytes.
	SizeGB int
}

// Inventory lists the golden images and the configured current_image.
// CurrentMissing is set when current_image names an image Tart does not have.
type Inventory struct {
	Current        string
	CurrentMissing bool
	Images         []Golden
}

type vmLister interface {
	List(ctx context.Context) ([]tart.VM, error)
}

// List reports the golden images Build produced, plus current_image when it
// has another name, newest first. It reads only; it never takes the image lock.
func List(ctx context.Context, path string) (Inventory, error) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return Inventory{}, err
	}
	return list(ctx, tart.Client{}, cfg.CurrentImage)
}

func list(ctx context.Context, client vmLister, current string) (Inventory, error) {
	inv := Inventory{Current: current}
	vms, err := client.List(ctx)
	if err != nil {
		return inv, err
	}
	inv.CurrentMissing = current != ""
	for _, vm := range vms {
		isCurrent := current != "" && vm.Name == current
		if !isCurrent && !goldenName.MatchString(vm.Name) {
			continue
		}
		if isCurrent {
			inv.CurrentMissing = false
		}
		inv.Images = append(inv.Images, Golden{Name: vm.Name, Current: isCurrent, Running: vm.Running, Created: builtOn(vm.Name), SizeGB: vm.Size})
	}
	slices.SortStableFunc(inv.Images, func(a, b Golden) int {
		return cmp.Or(b.Created.Compare(a.Created), cmp.Compare(b.Name, a.Name))
	})
	return inv, nil
}

// builtOn returns the local build date encoded in a golden image name.
func builtOn(name string) time.Time {
	if !goldenName.MatchString(name) {
		return time.Time{}
	}
	day, err := time.ParseInLocation("20060102", name[len("offscreen-"):len("offscreen-20060102")], time.Local)
	if err != nil {
		return time.Time{}
	}
	return day
}
