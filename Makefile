BINARY  := wd-control-tool
PKG     := ./cmd/wd-control-tool
# -linkmode=external is required to actually get a dynamically-linked
# glibc binary (verona-tool style) - CGO_ENABLED=1 alone is not
# enough when there's no real `import "C"` code to force external
# linking; without it the Go linker silently falls back to its
# internal linker and produces a static binary instead. Confirmed by
# checking `file`/`readelf -d` on the output - always do that after
# touching these flags.
LDFLAGS := -s -w -linkmode=external

.PHONY: build-ex2u build-ex4 build-ex2 strip verify-deps build-host test test-race fuzz clean

# EX2U (and, once its uRamdisk is rebuilt, EX2): Armada 385/370,
# armv7, hard-float ABI (confirmed: ld-linux-armhf.so.3).
build-ex2u:
	CGO_ENABLED=1 CC=arm-linux-gnueabihf-gcc GOOS=linux GOARCH=arm GOARM=7 \
		go build -ldflags "$(LDFLAGS)" -o $(BINARY)-armv7 $(PKG)

# EX4: Kirkwood-class, armv5, soft-float ABI (confirmed: ld-linux.so.3,
# not ld-linux-armhf.so.3). EX2's real silicon is armv7 too, but its
# CURRENT uRamdisk's binaries are armv5 soft-float - rebuild that
# uRamdisk against build-ex2u's output once this tool is ready, not
# against this target.
build-ex4:
	CGO_ENABLED=1 CC=arm-linux-gnueabi-gcc GOOS=linux GOARCH=arm GOARM=5 \
		go build -ldflags "$(LDFLAGS)" -o $(BINARY)-armv5 $(PKG)

# Alias kept for when EX2's own uRamdisk is rebuilt onto armv7 - until
# then this is identical to build-ex2u's output.
build-ex2: build-ex2u

# Fails the build if the resulting binary needs any shared library
# beyond the target's own minimal set (libc + the dynamic linker) -
# the whole point of this tool is dropping the vendor .so dependency.
verify-deps: build-ex2u build-ex4
	@for f in $(BINARY)-armv7 $(BINARY)-armv5; do \
		echo "=== $$f ==="; \
		deps=$$(arm-linux-gnueabihf-readelf -d $$f 2>/dev/null | grep NEEDED || true); \
		echo "$$deps"; \
		if echo "$$deps" | grep -qvE 'libc\.so|^$$'; then \
			echo "unexpected dependency in $$f" >&2; exit 1; \
		fi; \
	done

strip: verify-deps
	arm-linux-gnueabihf-strip $(BINARY)-armv7
	arm-linux-gnueabi-strip $(BINARY)-armv5
	@ls -la $(BINARY)-armv7 $(BINARY)-armv5

# Native build for local testing (not a release target).
build-host:
	go build -o $(BINARY) $(PKG)

# -timeout is explicit (not just relying on `go test`'s own 10m
# default) as defense in depth: a pty-based test blocking forever
# instead of failing cleanly is a real failure mode here, not a
# hypothetical one - see test/fakemcu's own doc comments on
# ReadFrame/readExactly for the one that actually happened.
TESTFLAGS := -timeout 120s

test:
	go vet ./...
	go test $(TESTFLAGS) ./...

# Several tests (pkg/mcu's pty-based ones especially) run the command
# under test in a goroutine concurrently with the test reading the
# "wire" - a race there is a real concurrency bug, not a test
# artifact. Not folded into the default `test` target because -race
# roughly doubles run time; run this before every push, not just
# while iterating locally.
test-race:
	go vet ./...
	go test -race $(TESTFLAGS) ./...


# Go can only fuzz one target per process, so this runs each
# Fuzz* function in this project for FUZZTIME apiece (default 30s;
# override with `make fuzz FUZZTIME=5m`). A plain `go test` (the
# `test`/`test-race` targets) always runs these too, but only against
# their small seed corpus - this is the opt-in, exploratory version.
FUZZTIME := 30s

fuzz:
	go test ./pkg/mcu/ -run=NONE -fuzz=FuzzEX4DateTimeFrame_NeverEmitsMarkerBytesInPayload -fuzztime=$(FUZZTIME)
	go test ./pkg/led/ -run=NONE -fuzz=FuzzPlan_NeverPanicsAndOnlyWritesKnownRegisters -fuzztime=$(FUZZTIME)
	go test ./pkg/fan/ -run=NONE -fuzz=FuzzEx2FanPlan_NeverPanicsAndOnlyWritesKnownRegisters -fuzztime=$(FUZZTIME)
	go test ./cmd/wd-control-tool/ -run=NONE -fuzz=FuzzParseArgs_NeverPanics -fuzztime=$(FUZZTIME)

clean:
	rm -f $(BINARY) $(BINARY)-armv7 $(BINARY)-armv5
