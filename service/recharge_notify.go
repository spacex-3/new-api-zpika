package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/shopspring/decimal"
)

type rechargeNotice struct {
	TopUpID           int
	CustomerID        int
	CustomerName      string
	Email             string
	PaidRMB           float64
	AddedQuota        int
	RemainingQuota    int
	CumulativePaidRMB float64
}

func loadRechargeNotice(tradeNo string) (*rechargeNotice, error) {
	var topUp model.TopUp
	if err := model.DB.Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
		return nil, err
	}
	if topUp.Status != common.TopUpStatusSuccess || topUp.Amount <= 0 {
		return nil, errors.New("top-up is not a completed wallet recharge")
	}

	var addedQuota int
	var err error
	switch topUp.PaymentProvider {
	case model.PaymentProviderAdmin, model.PaymentProviderCreem:
		addedQuota, err = common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(topUp.Amount))
	case model.PaymentProviderStripe:
		addedQuota, err = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
	default:
		addedQuota, err = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
	}
	if err != nil || addedQuota <= 0 {
		return nil, model.ErrInvalidTopUpQuota
	}

	var user model.User
	if err := model.DB.Select("id", "username", "display_name", "email", "quota").First(&user, topUp.UserId).Error; err != nil {
		return nil, err
	}
	name := strings.TrimSpace(user.DisplayName)
	if name == "" {
		name = user.Username
	}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		email = "-"
	}

	var cumulativePaidRMB float64
	if err := model.DB.Model(&model.TopUp{}).
		Where("user_id = ? AND status = ? AND amount > 0", user.Id, common.TopUpStatusSuccess).
		Select("COALESCE(SUM(money), 0)").Scan(&cumulativePaidRMB).Error; err != nil {
		return nil, err
	}

	return &rechargeNotice{
		TopUpID: topUp.Id, CustomerID: user.Id, CustomerName: name, Email: email,
		PaidRMB: topUp.Money, AddedQuota: addedQuota, RemainingQuota: user.Quota,
		CumulativePaidRMB: cumulativePaidRMB,
	}, nil
}

func renderRechargeNotice(lang string, notice *rechargeNotice) (string, string) {
	values := map[string]any{
		"Name": notice.CustomerName, "ID": notice.CustomerID, "Email": notice.Email,
		"PaidRMB":           fmt.Sprintf("%.2f", notice.PaidRMB),
		"AddedBalance":      logger.FormatQuota(notice.AddedQuota),
		"RemainingBalance":  logger.FormatQuota(notice.RemainingQuota),
		"CumulativePaidRMB": fmt.Sprintf("%.2f", notice.CumulativePaidRMB),
	}
	return i18n.Translate(lang, i18n.MsgNotifyRechargeSubject, values),
		i18n.Translate(lang, i18n.MsgNotifyRechargeContent, values)
}

// QueueRechargeNotification snapshots committed recharge data before returning
// to the payment callback, then sends Bark without delaying its response.
func QueueRechargeNotification(tradeNo string) {
	notice, err := loadRechargeNotice(tradeNo)
	if err != nil {
		common.SysLog(common.LogText("failed to load recharge notification for order %s: %s", tradeNo, err.Error()))
		return
	}
	gopool.Go(func() {
		notifyType := fmt.Sprintf("recharge_%d", notice.TopUpID)
		sentCount := notifyAdmins(notifyType, func(_ int, setting dto.UserSetting) bool {
			return setting.NotifyType == dto.NotifyTypeBark
		}, func(lang string) (string, string) {
			return renderRechargeNotice(lang, notice)
		}, true)
		common.SysLog(common.LogText("recharge notifications sent: %d", sentCount))
	})
}
