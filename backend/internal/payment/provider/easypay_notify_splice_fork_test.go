package provider

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// fork: 只用回调白名单内字段的签名拼接伪造（对应上游 #8075 描述的手法）。
//
// 攻击者先建订单 A（金额 M）拿到订单号，再建同金额的订单 B（跳转支付模式，
// 签名出现在浏览器可见的 submit.php 链接里），return_url 传
// "https://x&out_trade_no=<A>&pid=<P>&trade_status=TRADE_SUCCESS&type=a@<本站域名>/payment/result"。
// v0.3.11 的 CanonicalizeReturnURL 只校验 @ 之后的主机和路径，会原样保留这段
// userinfo。签名原文按字典序拼接且不转义，把 B 的下单字段重新切分到 name / type
// 两个回调字段里，就得到一条签名有效的「订单 A 已支付」回调：out_trade_no、pid、
// trade_status 都是干净的值，只有 name 和 type 含 & / =。v0.3.11 会接受它并给订单
// A 入账（已在 v0.3.11 上实测）。
//
// 现在两层都会拦：CanonicalizeReturnURL 拒绝带 userinfo 的 return_url（上游
// TestCanonicalizeReturnURLRejectsNonPlainHostAndUserinfo），VerifyNotification
// 要求除 name 外的取值不得含 & / =，而 type 必须吞下 return_url 末尾服务端追加的
// 查询串，必然含 &。后者与 notifyConfirm 无关，关闭查单确认后依然有效。
func TestEasyPayNotifyRejectsAllowlistedFieldSplice(t *testing.T) {
	t.Parallel()
	e := easyPayPoCProvider()
	e.config[easyPayNotifyConfirmKey] = easyPayNotifyConfirmOff

	const (
		pid         = "1000"
		money       = "650.00"
		targetOrder = "sub2_ORDER_A"
		orderB      = "sub2_ORDER_B"
	)
	prefix := "https://x"
	injected := "&out_trade_no=" + targetOrder + "&pid=" + pid + "&trade_status=" + tradeStatusSuccess + "&type=a"
	// "@" 之后是本站结果页，以及 buildPaymentReturnURL 追加的查询串（按键名排序，status 在最后）。
	tail := "@site.example.com/payment/result?order_id=99&out_trade_no=" + orderB + "&resume_token=tok&status=success"
	returnURL := prefix + injected + tail

	orderParams := map[string]string{
		"pid":          pid,
		"type":         "alipay",
		"out_trade_no": orderB,
		"notify_url":   e.config["notifyUrl"],
		"return_url":   returnURL,
		"name":         "balance recharge",
		"money":        money,
	}
	orderSign := easyPaySign(orderParams, e.config["pkey"])

	forged := map[string]string{
		"money":        money,
		"name":         "balance recharge&notify_url=" + e.config["notifyUrl"] + "&out_trade_no=" + orderB + "&pid=" + pid + "&return_url=" + prefix,
		"out_trade_no": targetOrder,
		"pid":          pid,
		"trade_status": tradeStatusSuccess,
		"type":         "a" + tail + "&type=alipay",
	}
	// 前提：伪造回调与订单 B 的签名原文逐字节相同，签名本身是有效的。
	if got := easyPaySign(forged, e.config["pkey"]); got != orderSign {
		t.Fatalf("PoC setup broken: forged sign %s != order sign %s", got, orderSign)
	}

	q := url.Values{}
	for k, v := range forged {
		q.Set(k, v)
	}
	q.Set("sign", orderSign)
	q.Set("sign_type", signTypeMD5)
	_, err := e.VerifyNotification(context.Background(), q.Encode(), nil)
	if err == nil {
		t.Fatal("spliced callback built from another order's signature must be rejected")
	}
	if !strings.Contains(err.Error(), "invalid notify param value: type") {
		t.Fatalf("expected the &/= value check to reject type, got: %v", err)
	}
}
