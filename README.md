# github.com/lnproxy/lnc

A minimalist client library for lnd,
originally developed for github.com/lnproxy/lnproxy.

## Example

See https://github.com/lnproxy/lnproxy/blob/main/cmd/http-relay/main.go
for an example of initializing a simple client.

## Permissions

To generate a minimal macaroon:
```
lncli bakemacaroon --save_to lnc.macaroon \
  uri:/lnrpc.Lightning/DecodePayReq \
  uri:/lnrpc.Lightning/LookupInvoice \
  uri:/invoicesrpc.Invoices/AddHoldInvoice \
  uri:/invoicesrpc.Invoices/SubscribeSingleInvoice \
  uri:/invoicesrpc.Invoices/CancelInvoice \
  uri:/invoicesrpc.Invoices/SettleInvoice \
  uri:/routerrpc.Router/SendPaymentV2 \
  uri:/routerrpc.Router/EstimateRouteFee \
  uri:/chainrpc.ChainKit/GetBestBlock
```
