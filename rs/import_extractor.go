package rs

/*
#include <stddef.h>
#include <stdint.h>
void gazelle_rs_ie_dispatch(const uint8_t *, size_t, uint8_t **, size_t *);
void gazelle_rs_ie_free(uint8_t *, size_t);
int32_t gazelle_rs_cargo_version_matches(const uint8_t *, size_t, const uint8_t *, size_t);
*/
import "C"

import (
	"fmt"
	pb "github.com/perplexityai/gazelle_rs/rs/proto"
	"google.golang.org/protobuf/proto"
	"unsafe"
)

// Use Cargo's version requirement semantics, including prerelease admission and
// implicit caret ranges, rather than translating them to another ecosystem.
func cargoVersionMatches(requirement, version string) (bool, error) {
	if requirement == "" || version == "" {
		return false, fmt.Errorf("empty Cargo version requirement or locked version")
	}
	req, ver := []byte(requirement), []byte(version)
	result := C.gazelle_rs_cargo_version_matches(
		(*C.uint8_t)(unsafe.Pointer(&req[0])), C.size_t(len(req)),
		(*C.uint8_t)(unsafe.Pointer(&ver[0])), C.size_t(len(ver)))
	if result < 0 {
		return false, fmt.Errorf("invalid Cargo version requirement %q or locked version %q", requirement, version)
	}
	return result == 1, nil
}

func extract(roots []string) ([]*pb.CrateResult, error) {
	req := &pb.Request{}
	for _, root := range roots {
		req.Crates = append(req.Crates, &pb.Crate{Root: root})
	}
	data, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	var input *C.uint8_t
	if len(data) > 0 {
		input = (*C.uint8_t)(unsafe.Pointer(&data[0]))
	}
	var output *C.uint8_t
	var size C.size_t
	C.gazelle_rs_ie_dispatch(input, C.size_t(len(data)), &output, &size)
	if output == nil {
		return nil, fmt.Errorf("extractor returned null")
	}
	defer C.gazelle_rs_ie_free(output, size)
	if uint64(size) > 1<<31-1 {
		return nil, fmt.Errorf("extractor response too large")
	}
	var response pb.Response
	if err := proto.Unmarshal(C.GoBytes(unsafe.Pointer(output), C.int(size)), &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, fmt.Errorf("%s", response.Error)
	}
	if len(response.Results) != len(roots) {
		return nil, fmt.Errorf("extractor returned %d results for %d crates", len(response.Results), len(roots))
	}
	return response.Results, nil
}
