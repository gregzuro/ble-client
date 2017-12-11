# ble-client

## Description

This process runs on a gateway, captures Bluetooth Low Energy Advertising packets, then sends the received data to Sixgill Sense 2.0 via their ingress API.

Only one instance of this process may be run at any time.

This process must be run with privilege (`sudo` or directly as the root user) in order to access the Bluetooth device.  

## Building

- clone repository
- `go get`
- `GOOS=linux go build`

## Getting

Get the latest release from [here](https://github.com/sixgill/ble-client/releases).

## Using

- scp the resulting binary to the target machine
- Change values in `ble-client-conf.json` as needed (parameters are described below), 
- Copy the `ble-client-conf.json` file to `~/.sense/`, and 
- Run `$ <sudo> ./ble-client <optional flags>`

## Parameters

| Parameter | Default Value | Meaning |
| --------: | :-----------: | :------ |
| sense-ingress-address | "" | IP address of the Sixgill Sense Ingress API server |
| sense-ingress-api-key | "" | API key for Sixgill Sense Ingress API server |

