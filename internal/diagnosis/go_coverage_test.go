package diagnosis

import "testing"

func TestGoCoverageDecoder(t *testing.T) {
	decoder := coverageArtifactDecoder{}
	valid := []byte("mode: atomic\nexample.org/repo/value.go:3.2,4.5 2 1\n")
	detection := decoder.Detect(ArtifactDescriptor{}, valid)
	if detection.Format != "go_coverprofile" || decoder.Decode(ArtifactDescriptor{}, valid, detection).Status != ArtifactDecoded {
		t.Fatal("Go coverage was not ingested")
	}
	for _, bad := range []string{
		"mode: atomic\nx.go:3.2,2.5 2 1",
		"mode: atomic\nx.go:3.2,4.5 2 -1",
		"mode: atomic\nx.go:3.2,4.5junk 2 1",
	} {
		if decoder.Decode(ArtifactDescriptor{}, []byte(bad), detection).Status != ArtifactMalformed {
			t.Errorf("accepted invalid profile: %s", bad)
		}
	}
}
