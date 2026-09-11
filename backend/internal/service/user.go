package service

import (
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID             int64
	Email          string
	Username       string
	Notes          string
	AvatarURL      string
	AvatarSource   string
	AvatarMIME     string
	AvatarByteSize int
	AvatarSHA256   string
	PasswordHash   string
	Role           string
	Balance        float64
	FrozenBalance  float64
	Concurrency    int
	Status         string
	AllowedGroups  []int64
	// RestrictPublicGroups narrows the public groups this user may bind to the
	// ones listed in AllowedGroups. False keeps the default, where every public
	// group is bindable.
	RestrictPublicGroups bool
	TokenVersion         int64 // Incremented on password change to invalidate existing tokens
	// TokenVersionResolved indicates TokenVersion already contains the fingerprint-derived
	// value expected in JWT claims and refresh-token state.
	TokenVersionResolved bool
	SignupSource         string
	LastLoginAt          *time.Time
	LastActiveAt         *time.Time
	LastUsedAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	DeletedAt            *time.Time // 非 nil 表示用户已软删除

	// GroupRates 用户专属分组倍率配置
	// map[groupID]rateMultiplier
	GroupRates map[int64]float64

	// TOTP 双因素认证字段
	TotpSecretEncrypted *string    // AES-256-GCM 加密的 TOTP 密钥
	TotpEnabled         bool       // 是否启用 TOTP
	TotpEnabledAt       *time.Time // TOTP 启用时间

	// 余额不足通知
	BalanceNotifyEnabled       bool
	BalanceNotifyThresholdType string // "fixed" (default) | "percentage"
	BalanceNotifyThreshold     *float64
	BalanceNotifyExtraEmails   []NotifyEmailEntry
	TotalRecharged             float64

	// RPMLimit 用户级每分钟请求数上限（0 = 不限制）。仅在所用分组未设置 rpm_limit
	// 且该 (用户, 分组) 无 rpm_override 时作为全局兜底生效，计数键 rpm:u:{userID}:{min}。
	RPMLimit int

	// UserGroupRPMOverride 来自 auth cache snapshot 的 (user, group) RPM 覆盖值，仅在
	// UserGroupRPMOverrideChecked 为 true 时才有意义（nil 在那种情况下表示"确认无 override"）。
	// 字段不持久化到数据库。
	UserGroupRPMOverride *int
	// UserGroupRPMOverrideChecked 标记 snapshot 构建时是否已经真正查过 (user, group) 的
	// RPM override（无论查到值还是确认为空）。为 false 时 checkRPM 必须回退查 DB，不能把
	// UserGroupRPMOverride==nil 误当作"确认无 override"——这是 auth cache snapshot 曾经
	// 存在的一个缺陷：只在查到非 nil override 时才写入字段，导致"确认无 override"（多数
	// 用户的常态）与"从未查过"都表现为 nil，每个请求都要重新查一次 DB，完全抵消缓存效果。
	UserGroupRPMOverrideChecked bool

	APIKeys       []APIKey
	Subscriptions []UserSubscription
}

func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func (u *User) IsActive() bool {
	return u.Status == StatusActive
}

// CanBindGroup checks whether a user can bind to a given group.
// For standard groups:
//   - Public groups (non-exclusive): bindable by every user, unless the user has
//     RestrictPublicGroups set, in which case the group must be in AllowedGroups
//   - Exclusive groups: only users with the group in AllowedGroups can bind
func (u *User) CanBindGroup(groupID int64, isExclusive bool) bool {
	// 公开分组（非专属）：默认所有用户都可以绑定；仅当该用户开启了公开分组
	// 限制时，才需要落在 AllowedGroups 中。
	if !isExclusive && !u.RestrictPublicGroups {
		return true
	}
	// 专属分组，以及受限用户的公开分组：需要在 AllowedGroups 中
	for _, id := range u.AllowedGroups {
		if id == groupID {
			return true
		}
	}
	return false
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}
