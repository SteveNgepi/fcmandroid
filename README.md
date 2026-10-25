# fcmandroid

Fake Android devices that receive Firebase Cloud Messaging (FCM) pushes, for
checking the targeting of a push backend against real, addressable devices.

A device is registered with Google the way an Android phone is (checkin, then
`register3`), gets an FCM token a backend can send to, and holds a connection to
Google to receive the pushes.

I needed to check how a push backend picks its audience, and the only honest way
is to have real devices receive the pushes: thousands, even tens of thousands of
them. A phone or an emulator per device does not work at that scale.

Then I found [cpz/firebase_recv](https://github.com/cpz/firebase_recv) (MIT),
which receives web FCM pushes. It showed me that a device does not have to be a
real one, and this project took off from there: parts of its code were taken and
adapted to register and run Android devices, each one a single cheap connection
to Google (about 40 KiB of memory) instead of a whole device. The MCS and
check-in protobufs originate in Chromium (BSD-licensed); their copyright header
is kept in `internal/pb`.

## Install

```sh
go get github.com/SteveNgepi/fcmandroid
```

Go 1.26 or newer.

## Quick start

You need the `google-services.json` of your own Firebase app and the SHA-1
fingerprint of its signing certificate.

```go
cfg, err := fcmandroid.NewAppConfig(googleServicesJSON, "com.example.app", certSHA1)
if err != nil {
	return err
}

device, err := fcmandroid.Provision(ctx, cfg, nil)
if err != nil {
	return err
}
// device.Token() is the address your backend sends to.

err = device.Listen(ctx, fcmandroid.ListenOptions{
	OnMessage: func(message fcmandroid.Message) error {
		// message.Data holds the keys of the push.
		return nil
	},
})
```

A device can be saved (`device.Identity()` marshals to JSON) and restored later
with `fcmandroid.NewDevice(identity)`. `ListenOptions.OnEvent` reports what
happens to a device: login, push, reset, deleted.

## Things to know

- `Provision` and `Listen` talk to Google and count against its limits. Use them
  with apps you own.
- `Listen` holds one session per device and never reconnects: retries, backoff
  and the rate of new connections are up to the caller.
- In our tests, a push sent while a device was not listening was not delivered
  to it later.
- An online device costs about 40 KiB of memory; 10,000 devices were held online
  at once from a single machine.
- This is not an official Google or Firebase project.

## License

MIT, see [LICENSE](LICENSE). Code taken from cpz/firebase_recv keeps its
copyright notice there.
