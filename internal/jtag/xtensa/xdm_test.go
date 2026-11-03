package xtensa

import (
	"errors"
	"testing"

	"github.com/ipoluianov/altesp/internal/jtag/chain"
	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
	"github.com/ipoluianov/altesp/internal/jtag/ftdi"
	"github.com/ipoluianov/altesp/internal/jtag/jtagsim"
)

const idcode = 0x120034E5

// The two TAP drivers in front of a simulated two-core Xtensa chip
func drivers(t *testing.T) map[string]func() (chain.Resetter, *jtagsim.Device) {
	cfg := jtagsim.Config{IDCode: idcode, Xtensa: &jtagsim.XtensaConfig{Cores: 2, OCDID: 0xAB}}
	return map[string]func() (chain.Resetter, *jtagsim.Device){
		"usb-jtag": func() (chain.Resetter, *jtagsim.Device) {
			d := jtagsim.New(cfg, nil)
			return espjtag.New(d), d
		},
		"ftdi": func() (chain.Resetter, *jtagsim.Device) {
			d := jtagsim.New(cfg, nil)
			l, err := ftdi.Open(d.FTDI(512), 0, 512)
			if err != nil {
				t.Fatal(err)
			}
			return ftdi.NewTAP(l), d
		},
	}
}

func TestChainAndXDM(t *testing.T) {
	for name, mk := range drivers(t) {
		t.Run(name, func(t *testing.T) {
			tap, dev := mk()
			ids, err := chain.ScanIDCodes(tap)
			if err != nil || len(ids) != 2 || ids[0] != idcode || ids[1] != idcode {
				t.Fatalf("chain %x %v", ids, err)
			}
			for target := 0; target < 2; target++ {
				c, err := chain.New(tap, []int{IRLen, IRLen}, target)
				if err != nil {
					t.Fatal(err)
				}
				x := New(c)
				p, err := x.ProbeCore()
				if err != nil || p.OCDID != 0xAB || p.PwrStat&PwrStatAllOn != PwrStatAllOn || p.DSR&DSRDebugOn == 0 {
					t.Fatalf("probe %v %v", p, err)
				}
				// the sticky reset bits were cleared by the probe
				if p2, _ := x.ProbeCore(); p2.PwrStat&(PwrStatCoreWasReset|PwrStatDebugWasReset) != 0 {
					t.Fatalf("PWRSTAT 0x%x", p2.PwrStat)
				}
				if err := x.Write(NARDCRSet, DCRStallInEn|uint32(target+1)<<8); err != nil {
					t.Fatal(err)
				}
				if v, err := x.Read(NARDCRSet); err != nil || v != DCRStallInEn|uint32(target+1)<<8 {
					t.Fatalf("DCR 0x%x %v", v, err)
				}
				if err := x.Write(NARDDR, 0xDEADBEEF); err != nil {
					t.Fatal(err)
				}
				if v, err := x.Read(NARDDR); err != nil || v != 0xDEADBEEF {
					t.Fatalf("DDR 0x%x %v", v, err)
				}
				if _, err := x.Read(0x7F); !errors.Is(err, ErrNARError) {
					t.Fatalf("bad register: %v", err)
				}
			}
			// each write went to its own TAP only
			for i := 0; i < 2; i++ {
				if dcr, pwr := dev.XDMState(i); dcr != DCRStallInEn|uint32(i+1)<<8 || pwr != PwrCtlAllOn {
					t.Fatalf("TAP %d: DCR 0x%x PWRCTL 0x%x", i, dcr, pwr)
				}
			}
		})
	}
}

func TestChainArgs(t *testing.T) {
	if _, err := chain.New(nil, []int{5}, 1); err == nil {
		t.Fatal("target outside the chain accepted")
	}
	long := make([]int, 13)
	for i := range long {
		long[i] = IRLen
	}
	if _, err := chain.New(nil, long, 0); err == nil {
		t.Fatal("65-bit IR accepted")
	}
}
