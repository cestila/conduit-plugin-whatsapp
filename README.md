# conduit-plugin-whatsapp

WhatsApp (multi-device) channel for Conduit, built on [whatsmeow](https://github.com/tulir/whatsmeow). Supports replies, reactions, polls, edit, delete, pin, read receipts, media and voice notes.

This is an official plugin for [ConduitOmniChat](https://github.com/cestila/ConduitOmniChat).
It is a standalone executable that `conduitd` launches and talks to over gRPC
(`conduit.v1.PluginService`). It describes its own UI through a manifest.

## Install

```sh
conduitd plugin install whatsapp
```

or use the **Plugins** panel of the Conduit desktop app. The binary is
downloaded from this repository's GitHub Releases and its SHA-256 checksum is
verified. See the [plugin documentation](https://github.com/cestila/ConduitOmniChat/blob/main/docs/PLUGINS.md)
for plugin directories and details.

## Credentials

No typed credentials: the account is linked by scanning a QR code (WhatsApp → Linked devices → Link a device). The account identifier you enter names the linked device.

## Build

```sh
go build -o conduit-plugin-whatsapp .
```

Copy the binary into one of the plugin directories of `conduitd`. To develop
against a local checkout of the SDK, add a temporary
`replace github.com/cestila/ConduitOmniChat/sdk => ../ConduitOmniChat/sdk` to
`go.mod` (do not commit it).

## Disclaimer

This plugin uses an **unofficial** client library and is not affiliated with or endorsed by Meta or WhatsApp. Using unofficial clients can violate WhatsApp's terms and **may get your account restricted or banned**. Use at your own risk.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
