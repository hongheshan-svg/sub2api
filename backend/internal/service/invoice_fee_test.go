package service

import "testing"

func TestInvoiceFeeShortfall(t *testing.T) {
	// 余额足够
	if ok, short := invoiceFeeShortfall(100, 60); !ok || short != 0 {
		t.Fatalf("enough: want ok=true short=0, got ok=%v short=%.2f", ok, short)
	}
	// 余额不足
	if ok, short := invoiceFeeShortfall(20, 60); ok || short != 40 {
		t.Fatalf("short: want ok=false short=40, got ok=%v short=%.2f", ok, short)
	}
	// 恰好相等
	if ok, short := invoiceFeeShortfall(60, 60); !ok || short != 0 {
		t.Fatalf("equal: want ok=true short=0, got ok=%v short=%.2f", ok, short)
	}
	// fee<=0:无需扣费
	if ok, short := invoiceFeeShortfall(0, 0); !ok || short != 0 {
		t.Fatalf("nofee: want ok=true short=0, got ok=%v short=%.2f", ok, short)
	}
}
