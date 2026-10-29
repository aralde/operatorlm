package update

import (
	"runtime"
	"testing"
)

func TestPickAssetsExactVariant(t *testing.T) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	base := "OperatorLM-" + runtime.GOOS + "-" + runtime.GOARCH
	rel := &releaseInfo{Assets: []releaseAsset{
		{Name: base + ext},
		{Name: base + "-headless" + ext},
		{Name: "checksums.txt"},
	}}
	bin, sums, err := pickAssets(rel)
	if err != nil {
		t.Fatal(err)
	}
	if want := base + variantSuffix + ext; bin.Name != want {
		t.Fatalf("picked %s, want %s", bin.Name, want)
	}
	if sums.Name != "checksums.txt" {
		t.Fatalf("checksums asset %s", sums.Name)
	}
}
