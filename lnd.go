package lnc

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/websocket"
)

type Lnd struct {
	Host      *url.URL
	Client    *http.Client
	TlsConfig *tls.Config
	Macaroon  string
}

func (lnd *Lnd) DecodeInvoice(invoice string) (*DecodedInvoice, error) {
	req, err := http.NewRequest(
		"GET",
		lnd.Host.JoinPath("v1/payreq", invoice).String(),
		nil,
	)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Grpc-Metadata-macaroon", lnd.Macaroon)

	resp, err := lnd.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("v1/payreq response: %s", string(body))
	}

	dec := json.NewDecoder(resp.Body)
	p := DecodedInvoice{}
	err = dec.Decode(&p)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return &p, nil
}

func (lnd *Lnd) AddInvoice(p InvoiceParameters) (string, error) {
	params, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	buf := bytes.NewBuffer(params)
	req, err := http.NewRequest(
		"POST",
		lnd.Host.JoinPath("v2/invoices/hodl").String(),
		buf,
	)
	if err != nil {
		return "", err
	}
	req.Header.Add("Grpc-Metadata-macaroon", lnd.Macaroon)
	resp, err := lnd.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var x interface{}
		dec := json.NewDecoder(resp.Body)
		err = dec.Decode(&x)
		if err != nil {
			return "", err
		}
		if x, ok := x.(map[string]interface{}); ok {
			if x["message"] == "invoice with payment hash already exists" {
				return "", PaymentHashExists
			}
		}
		return "", fmt.Errorf("v2/invoices/hodl  response: %#v", x)
	}
	dec := json.NewDecoder(resp.Body)
	pr := struct {
		PaymentRequest string `json:"payment_request"`
	}{}
	err = dec.Decode(&pr)
	if err != nil && err != io.EOF {
		return "", err
	}
	return pr.PaymentRequest, nil
}

func (lnd *Lnd) WatchInvoice(hash []byte) (uint64, error) {
	header := http.Header(make(map[string][]string, 1))
	header.Add("Grpc-Metadata-Macaroon", lnd.Macaroon)
	loc := *lnd.Host
	if loc.Scheme == "https" {
		loc.Scheme = "wss"
	} else {
		loc.Scheme = "ws"
	}
	origin := *lnd.Host
	origin.Scheme = "http"

	ws, err := websocket.DialConfig(&websocket.Config{
		Location:  loc.JoinPath("v2/invoices/subscribe", base64.URLEncoding.EncodeToString(hash)),
		Origin:    &origin,
		TlsConfig: lnd.TlsConfig,
		Header:    header,
		Version:   13,
	})
	if err != nil {
		return 0, err
	}
	defer ws.Close()
	err = websocket.JSON.Send(ws, struct{}{})
	if err != nil {
		return 0, err
	}
	for {
		message := struct {
			Result struct {
				State       string `json:"state"`
				AmtPaidMsat uint64 `json:"amt_paid_msat,string"`
			} `json:"result"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}{}
		err = websocket.JSON.Receive(ws, &message)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if message.Error.Message != "" {
			return 0, fmt.Errorf("v2/invoices/subscribe response: %s", message.Error.Message)
		}

		switch message.Result.State {
		case "OPEN":
			time.Sleep(500 * time.Millisecond)
		case "ACCEPTED":
			return message.Result.AmtPaidMsat, nil
		case "SETTLED", "CANCELED":
			return message.Result.AmtPaidMsat, fmt.Errorf("invoice %s before payment", message.Result.State)
		default:
			return 0, fmt.Errorf("v2/invoices/subscribe unhandled state: %s", message.Result.State)
		}

		if err == io.EOF {
			return 0, err
		}
	}
}

func (lnd *Lnd) CancelInvoice(hash []byte) error {
	params, _ := json.Marshal(
		struct {
			PaymentHash []byte `json:"payment_hash"`
		}{
			PaymentHash: hash,
		},
	)
	buf := bytes.NewBuffer(params)
	req, err := http.NewRequest(
		"POST",
		lnd.Host.JoinPath("v2/invoices/cancel").String(),
		buf,
	)
	if err != nil {
		return err
	}
	req.Header.Add("Grpc-Metadata-macaroon", lnd.Macaroon)
	resp, err := lnd.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var x interface{}
	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(&x)
	if err != nil && err != io.EOF {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("v2/invoices/cancel response: %v\n", x)
	}
	if xmap, ok := x.(map[string]interface{}); !ok || len(xmap) != 0 {
		return fmt.Errorf("v2/invoices/cancel unhandled response: %v\n", x)
	}

	return nil
}

func (lnd *Lnd) PayInvoice(params PaymentParameters) ([]byte, error) {
	header := http.Header(make(map[string][]string, 1))
	header.Add("Grpc-Metadata-Macaroon", lnd.Macaroon)
	loc := *lnd.Host
	if loc.Scheme == "https" {
		loc.Scheme = "wss"
	} else {
		loc.Scheme = "ws"
	}
	q := url.Values{}
	q.Set("method", "POST")
	loc.RawQuery = q.Encode()
	origin := *lnd.Host
	origin.Scheme = "http"

	ws, err := websocket.DialConfig(&websocket.Config{
		Location:  loc.JoinPath("v2/router/send"),
		Origin:    &origin,
		TlsConfig: lnd.TlsConfig,
		Header:    header,
		Version:   13,
	})
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	err = websocket.JSON.Send(ws, struct {
		PaymentParameters
		NoInflightUpdates bool    `json:"no_inflight_updates"`
		Amp               bool    `json:"amp"`
		TimePref          float64 `json:"time_pref"`
	}{
		PaymentParameters: params,
		NoInflightUpdates: true,
		Amp:               false,
		TimePref:          0.9,
	})
	if err != nil {
		return nil, err
	}
	for {
		message := struct {
			Result struct {
				Status        string `json:"status"`
				PreImage      string `json:"payment_preimage"`
				FailureReason string `json:"failure_reason"`
			} `json:"result"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}{}
		err = websocket.JSON.Receive(ws, &message)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if message.Error.Message != "" {
			return nil, fmt.Errorf("v2/router/send response: %s", message.Error.Message)
		}

		switch message.Result.Status {
		case "FAILED":
			return nil, errors.New("payment failed")
		case "UNKNOWN", "IN_FLIGHT", "":
			time.Sleep(500 * time.Millisecond)
		case "SUCCEEDED":
			return hex.DecodeString(message.Result.PreImage)
		default:
			return nil, fmt.Errorf("v2/router/send unhandled status: %s", message.Result.Status)
		}

		if err == io.EOF {
			return nil, err
		}
	}
}

func (lnd *Lnd) SettleInvoice(preimage []byte) error {
	params, err := json.Marshal(struct {
		PreImage []byte `json:"preimage"`
	}{
		PreImage: preimage,
	})
	if err != nil {
		return err
	}
	buf := bytes.NewBuffer(params)
	req, err := http.NewRequest(
		"POST",
		lnd.Host.JoinPath("v2/invoices/settle").String(),
		buf,
	)
	if err != nil {
		return err
	}
	req.Header.Add("Grpc-Metadata-macaroon", lnd.Macaroon)
	resp, err := lnd.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var x interface{}
	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(&x)
	if err != nil && err != io.EOF {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("v2/invoices/settle response: %#v", x)
	}
	if xmap, ok := x.(map[string]interface{}); !ok || len(xmap) != 0 {
		return fmt.Errorf("v2/invoices/settle unhandled response: %#v", x)
	}
	return nil
}

func (lnd *Lnd) EstimateRoutingFee(invoice_params DecodedInvoice, amount_msat uint64) (uint64, uint64, error) {
	if invoice_params.NumMsat == 0 && amount_msat == 0 {
		return 0, 0, errors.New("need a non-zero amount to estimate fee")
	} else if invoice_params.NumMsat > 0 {
		amount_msat = invoice_params.NumMsat
	}

	height, err := lnd.getBlockHeight()
	if err != nil {
		return 0, 0, err
	}
	fee_msat, cltv, errs := lnd.estimateRoutingFee(invoice_params.Destination, amount_msat)
	cltv_delta := cltv - height
	for _, route_hint := range invoice_params.RouteHints {
		if len(route_hint.HopHints) == 0 {
			errs = errors.Join(errs, errors.New("zero hops in route hint"))
			continue
		}
		f, c, e := lnd.estimateRoutingFee(route_hint.HopHints[0].NodeId, amount_msat)
		errs = errors.Join(errs, e)
		if e != nil {
			continue
		}
		c -= height
		for _, hop := range route_hint.HopHints {
			f += hop.FeeBaseMsat + (amount_msat*hop.FeePPM)/1_000_000
			c += hop.CltvExpiryDelta
		}
		if f < fee_msat {
			fee_msat = f
			cltv_delta = c
		}
	}
	if fee_msat == 18446744073709551615 || cltv_delta == 18446744073709551615 {
		return 0, 0, errors.Join(errs, errors.New("could not find route"))
	}

	return fee_msat, cltv_delta + invoice_params.CltvExpiry, nil
}

func (lnd *Lnd) estimateRoutingFee(destination string, amount_msat uint64) (uint64, uint64, error) {
	destination_bytes, err := hex.DecodeString(destination)
	if err != nil {
		return 18446744073709551615, 18446744073709551615, err
	}

	params, err := json.Marshal(struct {
		Destination []byte `json:"dest"`
		AmountSat   uint64 `json:"amt_sat,string"`
	}{
		Destination: destination_bytes,
		AmountSat:   (amount_msat + 999) / 1000,
	})
	if err != nil {
		return 18446744073709551615, 18446744073709551615, err
	}
	buf := bytes.NewBuffer(params)
	req, err := http.NewRequest(
		"POST",
		lnd.Host.JoinPath("v2/router/route/estimatefee").String(),
		buf,
	)
	if err != nil {
		return 18446744073709551615, 18446744073709551615, err
	}
	req.Header.Add("Grpc-Metadata-macaroon", lnd.Macaroon)
	resp, err := lnd.Client.Do(req)
	if err != nil {
		return 18446744073709551615, 18446744073709551615, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 18446744073709551615, 18446744073709551615, err
		}
		return 18446744073709551615, 18446744073709551615, fmt.Errorf("v2/router/route/estimatefee  response: %s", string(body))
	}

	dec := json.NewDecoder(resp.Body)
	estimate := struct {
		RoutingFeeMsat uint64 `json:"routing_fee_msat,string"`
		TimeLockDelay  uint64 `json:"time_lock_delay,string"`
	}{}
	err = dec.Decode(&estimate)
	if err != nil && err != io.EOF {
		return 18446744073709551615, 18446744073709551615, err
	}

	return estimate.RoutingFeeMsat, estimate.TimeLockDelay, nil
}

func (lnd *Lnd) getBlockHeight() (uint64, error) {
	req, err := http.NewRequest(
		"GET",
		lnd.Host.JoinPath("v2/chainkit/bestblock").String(),
		nil,
	)
	if err != nil {
		return 0, err
	}
	req.Header.Add("Grpc-Metadata-macaroon", lnd.Macaroon)

	resp, err := lnd.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("v2/chainkit/bestblock: %s", string(body))
	}

	dec := json.NewDecoder(resp.Body)

	r := struct {
		BlockHeight uint64 `json:"block_height"`
	}{}
	err = dec.Decode(&r)
	if err != nil && err != io.EOF {
		return 0, err
	}
	return r.BlockHeight, nil
}
