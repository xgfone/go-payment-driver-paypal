// Copyright 2026 xgfone
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package paypal

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xgfone/go-currency"
	"github.com/xgfone/go-payment-driver/builder"
	"github.com/xgfone/go-payment-driver/driver"
	"github.com/xgfone/go-toolkit/jsonx"
	paypal "github.com/xgfone/plutov-paypal"
)

func init() {
	registerBuilder("qrcode", func(b builder.Builder, c Config) (driver.Driver, error) {
		driver, err := newDriver(b, c)
		return &Driver{_Driver: driver}, err
	})
}

type Driver struct{ _Driver }

func (d *Driver) CreatePayment(ctx context.Context, req driver.CreatePaymentRequest) (info driver.PayLinkInfo, err error) {
	if !d.metadata.CurrencyIsSupported(req.PaymentCurrency) {
		return info, driver.ErrUnsupported.WithErrorf("unsupported currency '%s'", req.PaymentCurrency)
	}

	amount, err := currency.FormatMinorToMajor(req.PaymentAmount, req.PaymentCurrency)
	if err != nil {
		return
	}

	purchaseUnit := paypal.PurchaseUnitRequest{
		Description: req.PaymentDesc,
		ReferenceID: req.PaymentId,
		InvoiceID:   req.PaymentId,
		CustomID:    req.PaymentId,
		Amount: &paypal.PurchaseUnitAmount{
			Currency: req.PaymentCurrency,
			Value:    amount,
		},
	}
	paymentSource := paypal.PaymentSourcePaypal{
		ExperienceContext: paypal.PaymentSourcePaypalExperienceContext{
			ReturnURL:  d.config.ReturnUrl,
			CancelURL:  d.config.CancelUrl,
			UserAction: d.config.UserAction,

			BrandName: d.config.BrandName,
			Locale:    d.config.Locale,
		},
	}

	const intent = paypal.OrderIntentCapture
	punits := []paypal.PurchaseUnitRequest{purchaseUnit}
	source := &paypal.PaymentSource{Paypal: &paymentSource}
	order, err := d.client.CreateOrderWithPaypalRequestID(ctx, intent, punits, source, nil, req.PaymentId)
	if err != nil {
		return
	}

	for _, link := range order.Links {
		if link.Rel == "approve" || link.Rel == "payer-action" {
			info.PayLink = link.Href
			break
		}
	}
	if info.PayLink == "" {
		err = errors.New("no paypal approved links")
		return
	}

	info.ChannelPaymentId = order.ID
	return
}

func (d *Driver) QueryPayment(ctx context.Context, req driver.QueryPaymentRequest) (info driver.PaymentInfo, ok bool, err error) {
	order, err := d.client.GetOrder(ctx, req.ChannelPaymentId)
	if err != nil {
		if e, ok := err.(*paypal.ErrorResponse); ok && e.Response.StatusCode == 404 {
			err = nil
		}
		return
	}

	ok = true
	info, err = d.orderToPaymentInfo(ctx, order)
	return
}

func (d *Driver) CancelPayment(ctx context.Context, req driver.CancelPaymentRequest) error {
	pi, ok, err := d.QueryPayment(ctx, driver.QueryPaymentRequest{
		PaymentId:        req.PaymentId,
		ChannelPaymentId: req.ChannelPaymentId,
		ChannelData:      req.ChannelData,
	})
	if err != nil || !ok {
		return err
	}

	switch pi.TaskStatus {
	case driver.TaskStatusSuccess:
		return driver.ErrPaid

	case driver.TaskStatusClosed, driver.TaskStatusFailure:
		return nil

	default:
		// const s = "paypal orders v2 does not provide a generic cancel endpoint; let the order expire naturally"
		// return driver.ErrUnallowed.WithReason(s)

		// We do not return an error.
		return nil
	}
}

func (d *Driver) RefundPayment(ctx context.Context, req driver.CreateRefundRequest) (info driver.RefundInfo, err error) {
	if !currency.IsSupported(req.PaymentCurrency) {
		return info, driver.ErrUnsupported.WithReasonf("unsupported currency: %s", req.PaymentCurrency)
	}

	channeldata := driver.DecodeChannelData[ChannelData](req.ChannelData)
	if channeldata.CaptureId == "" {
		pay, ok, err := d.QueryPayment(ctx, driver.QueryPaymentRequest{
			PaymentId:        req.PaymentId,
			ChannelPaymentId: req.ChannelPaymentId,
			ChannelData:      req.ChannelData,
		})
		if err != nil {
			return info, err
		}
		if !ok {
			return info, driver.ErrBadRequest.WithReason("payment not found")
		}
		if pay.TaskStatus != driver.TaskStatusSuccess {
			return info, driver.ErrUnallowed.WithReason("paypal payment is not captured successfully")
		}

		channeldata = driver.DecodeChannelData[ChannelData](pay.ChannelData)
		if channeldata.CaptureId == "" {
			return info, driver.ErrUnallowed.WithReason("paypal capture id not found")
		}
	}

	amount, err := currency.FormatMinorToMajor(req.RefundAmount, req.PaymentCurrency)
	if err != nil {
		return info, driver.ErrBadRequest.WithReason(err.Error())
	}

	currency := strings.ToUpper(req.PaymentCurrency)
	_amount := paypal.Money{Currency: currency, Value: amount}
	_req := paypal.RefundCaptureRequest{
		Amount:      &_amount,
		CustomID:    req.PaymentId,
		InvoiceID:   req.RefundId,
		NoteToPayer: req.RefundReason,
	}
	_resp, err := d.client.RefundCapture(ctx, channeldata.CaptureId, _req)
	if err != nil {
		return
	}

	info = d.refundToRefundInfo(_resp)
	return
}

func (d *Driver) QueryRefund(ctx context.Context, req driver.QueryRefundRequest) (info driver.RefundInfo, ok bool, err error) {
	url := fmt.Sprintf("%s%s", d.client.APIBase, "/v2/payments/refunds/"+req.ChannelRefundId)
	_req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}

	var refund paypal.RefundResponse
	if err = d.client.SendWithAuth(_req, &refund); err != nil {
		if e, ok := err.(*paypal.ErrorResponse); ok && e.Response.StatusCode == 404 {
			err = nil
		}
		return
	}

	info = d.refundToRefundInfo(&refund)
	return
}

func (d *Driver) ParseCallbackRequest(ctx context.Context, req driver.CallbackRequest) (cbinfo driver.CallbackInfo, err error) {
	_resp, err := d.client.VerifyWebhookSignature(ctx, req.Request, d.config.WebhookId)
	if err != nil {
		return
	} else if _resp.VerificationStatus != "SUCCESS" {
		err = errors.New("invalid paypal webhook signature")
		return
	}

	type Event struct {
		paypal.Event

		Resource json.RawMessage `json:"resource"`
	}

	var event Event
	if err = jsonx.UnmarshalReader(&event, req.Request.Body); err != nil {
		return
	}

	switch event.EventType {
	case "CHECKOUT.ORDER.APPROVED": // Payer has paid and we need to capture the payment.
		var order paypal.Order
		if err = jsonx.Unmarshal(event.Resource, &order); err != nil {
			return
		}

		var info driver.PaymentInfo
		if info, err = d.orderToPaymentInfo(ctx, &order); err == nil {
			cbinfo.PaymentInfo = &info
			cbinfo.Type = driver.CallbackTypePayment
		}

	case "CHECKOUT.PAYMENT-APPROVAL.REVERSED": // Expired and Cancelled by PayPal
		var order struct {
			paypal.Order
			OrderID string `json:"order_id"`
		}
		if err = jsonx.Unmarshal(event.Resource, &order); err != nil {
			return
		}

		cbinfo.Type = driver.CallbackTypePayment
		cbinfo.PaymentInfo = &driver.PaymentInfo{
			ChannelPaymentId: cmp.Or(order.OrderID, order.ID),
			ChannelStatus:    "REVERSED",

			FailReason: "payment approval reversed before capture",
			TaskStatus: driver.TaskStatusFailure,
		}
		if len(order.PurchaseUnits) > 0 {
			punit := order.PurchaseUnits[0]
			cbinfo.PaymentInfo.PaymentId = cmp.Or(punit.CustomID, punit.ReferenceID)
		}

	case
		"PAYMENT.CAPTURE.DENIED",    // Cancelled By PayPal
		"PAYMENT.CAPTURE.DECLINED",  // Cancelled By IssuingBank
		"PAYMENT.CAPTURE.COMPLETED": // Success

		var capture paypal.CaptureAmount
		if err = jsonx.Unmarshal(event.Resource, &capture); err != nil {
			return
		}

		info := d.captureToPaymentInfo("", paypal.PayerWithNameAndPhone{}, capture)
		cbinfo.PaymentInfo = &info
		cbinfo.Type = driver.CallbackTypePayment

	case "PAYMENT.CAPTURE.REFUNDED": // Refund Success
	// Since the Capture object in the PayPal event notification
	// does not contain the refund ID, we cannot determine which request
	// this refund corresponds to. Therefore, we do not process this event
	// notification; instead, we wait for the upper-layer business logic
	// to proactively query the refund status based on the refund ID.
	/*
		var capture paypal.CaptureAmount
		if err = jsonx.UnmarshalBytes(event.Resource, &capture); err != nil {
			return
		}

		cd := ChannelData{CaptureId: capture.ID}
		cbinfo.Type = driver.CallbackTypeRefund
		cbinfo.RefundInfo = &driver.RefundInfo{
			PaymentId:     cmp.Or(capture.CustomID, capture.InvoiceID),
			ChannelStatus: capture.Status,
			ChannelData:   driver.EncodeChannelData(cd),
			TaskStatus:    driver.TaskStatusSuccess,
			RefundReason:  capture.StatusDetails.Reason,
		}
	*/

	/// Refund
	case "PAYMENT.REFUND.FAILED": // Refund Failure
		var refund paypal.RefundResponse
		if err = json.Unmarshal(event.Resource, &refund); err != nil {
			return
		}

		info := d.refundToRefundInfo(&refund)
		cbinfo.RefundInfo = &info
		cbinfo.Type = driver.CallbackTypeRefund
	}

	return
}

func (d *Driver) SendCallbackResponse(_ context.Context, rw http.ResponseWriter, err error) {
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}

	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte(`{"received":true}`))
}

/// ----------------------------------------------------------------------- ///

func (d *Driver) orderToPaymentInfo(ctx context.Context, order *paypal.Order) (info driver.PaymentInfo, err error) {
	punit := order.PurchaseUnits[0]
	info = driver.PaymentInfo{
		PaymentId: cmp.Or(punit.CustomID, punit.InvoiceID, punit.ReferenceID),

		ChannelPaymentId: order.ID,
		ChannelStatus:    order.Status,
	}

	switch strings.ToUpper(order.Status) {
	case paypal.OrderStatusVoided: // "VOIDED"
		info.TaskStatus = driver.TaskStatusClosed

	case paypal.OrderStatusCreated, paypal.OrderStatusSaved, "PAYER_ACTION_REQUIRED":
		info.TaskStatus = driver.TaskStatusProcessing

	case paypal.OrderStatusApproved: // "APPROVED"
		info.TaskStatus = driver.TaskStatusProcessing

		var resp *paypal.CaptureOrderResponse
		resp, err = d.client.CaptureOrder(ctx, order.ID, paypal.CaptureOrderRequest{})
		if err != nil {
			return
		} else if resp.Status == paypal.OrderStatusCompleted {
			info = d.captureToPaymentInfo(order.ID, resp.Payer, resp.PurchaseUnits[0].Payments.Captures[0])
		}

	case paypal.OrderStatusCompleted: // "COMPLETED"
		info = d.captureToPaymentInfo(order.ID, order.Payer, punit.Payments.Captures[0])

	default:
		info.TaskStatus = driver.TaskStatusUnknown
	}

	return
}

func (d *Driver) captureToPaymentInfo(orderId string, payer paypal.PayerWithNameAndPhone, cap paypal.CaptureAmount) driver.PaymentInfo {
	channeldata := driver.EncodeChannelData(ChannelData{CaptureId: cap.ID})
	payerid := strings.Join([]string{payer.PayerID, payer.EmailAddress}, ",")

	info := driver.PaymentInfo{
		PaymentId: cmp.Or(cap.CustomID, cap.InvoiceID),

		ChannelPaymentId: orderId,
		ChannelStatus:    cap.Status,
		ChannelData:      channeldata,

		PayerId: payerid,

		FailReason: cap.StatusDetails.Reason,
	}

	switch strings.ToUpper(cap.Status) {
	case "REFUNDED", "PARTIALLY_REFUNDED":
		info.TaskStatus = driver.TaskStatusSuccess
		info.IsRefunded = true

	case "COMPLETED":
		info.TaskStatus = driver.TaskStatusSuccess

	case "PENDING":
		info.TaskStatus = driver.TaskStatusProcessing

	case "DECLINED", "FAILED":
		info.TaskStatus = driver.TaskStatusFailure

	default:
		info.TaskStatus = driver.TaskStatusUnknown
	}

	info.PayerPaidCurrency = strings.ToUpper(cap.Amount.Currency)
	if cap.Amount.Value != "" {
		info.PayerPaidAmount, _ = currency.ParseMajorToMinor(cap.Amount.Value, cap.Amount.Currency)
	}

	if !cap.UpdateTime.IsZero() {
		info.PayerPaidAt = cap.UpdateTime
	} else if !cap.CreateTime.IsZero() {
		info.PayerPaidAt = cap.CreateTime
	}

	return info
}

func (d *Driver) refundToRefundInfo(refund *paypal.RefundResponse) (info driver.RefundInfo) {
	info.PaymentId = refund.CustomID
	info.RefundId = refund.InvoiceID

	info.RefundReason = refund.NoteToPayer
	info.ChannelRefundId = refund.ID
	info.ChannelStatus = refund.Status
	switch refund.Status {
	case "FAILED":
		info.TaskStatus = driver.TaskStatusFailure
		info.FailReason = refund.StatusDetails.Reason

	case "CANCELLED":
		info.TaskStatus = driver.TaskStatusFailure

	case "PENDING":
		info.TaskStatus = driver.TaskStatusProcessing

	case "COMPLETED":
		info.TaskStatus = driver.TaskStatusSuccess
		info.RefundedAt = refund.CreateTime

	default:
		info.TaskStatus = driver.TaskStatusUnknown
	}

	return
}
