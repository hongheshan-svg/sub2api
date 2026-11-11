package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// 回归测试：通知标题此前按字节截断到 255，中文标题超过 85 个字就会被切，
// 切点落在字符中间时产生非法 UTF-8，Postgres 会拒绝整条 INSERT。
func TestTruncateNotificationField_CountsCharactersNotBytes(t *testing.T) {
	short := "您的开票申请已完成"
	require.Equal(t, short, truncateNotificationField(short, notificationTitleMaxChars))

	// 100 个汉字 = 300 字节：列宽 255 个字符放得下，不应被截。
	hundred := strings.Repeat("票", 100)
	require.Equal(t, hundred, truncateNotificationField(hundred, notificationTitleMaxChars))

	// 混合内容超长：截到 255 个字符，且仍是合法 UTF-8（按字节切到 255 会落在"票"中间）。
	long := "a" + strings.Repeat("票", 300)
	got := truncateNotificationField(long, notificationTitleMaxChars)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, notificationTitleMaxChars, utf8.RuneCountInString(got))
	require.True(t, strings.HasPrefix(long, got))

	ascii := strings.Repeat("x", 600)
	require.Len(t, truncateNotificationField(ascii, notificationLinkMaxChars), notificationLinkMaxChars)
}
