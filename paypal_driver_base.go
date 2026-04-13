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
	"github.com/xgfone/go-payment-driver/builder"
	"github.com/xgfone/go-payment-driver/driver"
	paypal "github.com/xgfone/plutov-paypal"
)

func registerBuilder(scene string, newf builder.DriverNewer[Config]) {
	metadata := driver.NewMetadata(Type, scene).WithLinkType(driver.LinkTypeCodeUrl)
	builder.Register(builder.New(newf, metadata))
}

func newDriver(b builder.Builder, c Config) (d _Driver, err error) {
	if err = c.init(); err != nil {
		return
	}

	client, err := paypal.NewClient(c.ClientId, c.ClientSecret, "https://api-m.paypal.com")
	if err != nil {
		return
	}

	if c.Sandbox {
		client.APIBase = "https://api-m.sandbox.paypal.com"
	}
	client.SetReturnRepresentation()

	metadata := b.Metadata()
	metadata.Currencies = c.Currencies

	d = _Driver{metadata: metadata, config: c, client: client}
	return
}

type _Driver struct {
	metadata driver.Metadata
	config   Config
	client   *paypal.Client
}

func (d *_Driver) Metadata() driver.Metadata {
	return d.metadata
}
