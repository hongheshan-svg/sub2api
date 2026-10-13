package service

import (
	"errors"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/collection"
)

func TestProvideTimingWheelService_ReturnsError(t *testing.T) {
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, _ collection.Execute) (*collection.TimingWheel, error) {
		return nil, errors.New("boom")
	}

	svc, err := ProvideTimingWheelService()
	if err == nil {
		t.Fatalf("期望返回 error，但得到 nil")
	}
	if svc != nil {
		t.Fatalf("期望返回 nil svc，但得到非空")
	}
}

func TestProvideTimingWheelService_Success(t *testing.T) {
	svc, err := ProvideTimingWheelService()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	if svc == nil {
		t.Fatalf("期望 svc 非空，但得到 nil")
	}
	svc.Stop()
}

// 发票依赖必须由 provider 注入：之前这几个 setter 只手写在 wire_gen.go 里，
// 重新生成 Wire 后会被静默丢掉，编译照过但发票通知/邮件不再发出。
func TestProvidePaymentService_WiresInvoiceDependencies(t *testing.T) {
	notificationService := &NotificationService{}
	settingService := &SettingService{}
	emailService := &EmailService{}
	userService := &UserService{}

	svc := ProvidePaymentService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		notificationService, settingService, emailService, userService)

	if svc.invoiceNotifier != notificationService {
		t.Fatalf("invoiceNotifier 未注入")
	}
	if svc.invoiceSettingService != settingService {
		t.Fatalf("invoiceSettingService 未注入")
	}
	if svc.invoiceEmailSender != emailService {
		t.Fatalf("invoiceEmailSender 未注入")
	}
	if svc.userService != userService {
		t.Fatalf("userService 未注入")
	}
}
