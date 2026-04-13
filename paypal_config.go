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

import "errors"

const Type = "paypal"

type Config struct {
	ClientId     string
	ClientSecret string

	// PayPal webhook id, not secret.
	// Used by /v1/notifications/verify-webhook-signature.
	WebhookId string

	// For PayPal Checkout payer return.
	ReturnUrl string
	CancelUrl string

	// Optional experience fields.
	Locale     string `json:",omitempty"`
	BrandName  string `json:",omitempty"`
	UserAction string `json:",omitempty"` // default: PAY_NOW

	Currencies []string

	Sandbox bool `json:",omitempty"`
}

func (c *Config) Init() error {
	return c.init()
}

func (c *Config) init() (err error) {
	if c.UserAction == "" {
		c.UserAction = "PAY_NOW"
	}

	if len(c.Currencies) == 0 {
		return errors.New("missing Currencies")
	}

	return
}

type ChannelData struct {
	CaptureId string `json:",omitempty"`
}
