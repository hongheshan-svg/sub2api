package provider

import "strings"

// fork: 回调入账前的上游查单确认（ConfirmNotification）依赖易支付的
// api.php?act=order。支付FM 等只兼容易支付下单与回调规范的平台没有这个
// 查单接口，确认必然失败、订单无法自动到账，所以允许按实例关闭。
//
// 关闭后入账只依据验签通过的回调；VerifyNotification 的防伪造加固
// （回调参数白名单、pid 绑定、除 name 外的取值不得含 & / =）不受影响。
const (
	easyPayNotifyConfirmKey = "notifyConfirm"
	easyPayNotifyConfirmOff = "off"
)

// notifyConfirmDisabled 只有显式配置为 off 才关闭；未配置或无法识别的取值
// 一律按开启处理。
func (e *EasyPay) notifyConfirmDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(e.config[easyPayNotifyConfirmKey]), easyPayNotifyConfirmOff)
}
