package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"os/user"
	"reflect"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/currantlabs/ble"

	"github.com/currantlabs/ble/examples/lib/dev"

	"gopkg.in/resty.v1"

	pb "github.com/sixgill/sense-ingress-api/proto"
	"github.com/uudashr/iso8601"
)

var (
	device          = "default" //flag.String("device", "default", "implementation of ble")
	dup             = true      // flag.Bool("dup", true, "allow duplicate reported")
	jwt             string
	temperatureUnit = "C"
)

// BleDevicePattern contains all the elements necessary to identify devices of a type
type BleDevicePattern struct {
	Name string
	Svcs []ble.UUID
}

// HumiTempData defines the format of the data received from these sensors
type HumiTempData struct {
	TemperatureSign     uint8
	TemperatureWhole    uint8
	TemperatureFraction uint8
	HumiditySign        uint8
	HumidityWhole       uint8
	HumidityFraction    uint8
	UpdateRate          uint8
	Address             [6]byte
}

// BleData is the format of the data that we will send to Sense 2.0
type BleData struct {
	Address     string       `json:"address"`
	Rssi        int          `json:"rssi"`
	ID          string       `json:"id"`
	NodeID      string       `json:"nodeID"`
	Timestamp   iso8601.Time `json:"timestamp_iso8601"`
	SensorValue float64      `json:"sensor_value"`
	Units       string       `json:"units"`
}

// HumiTemp contains the pattern for matching this device type, plus a place to put the received data
var pattern = BleDevicePattern{
	Name: "HumiTemp Sensor Tag",
	Svcs: []ble.UUID{[]byte{0x20, 0xaa}},
}

var senseIngressAddress *string

// Config contains all of our parameters
type Config struct {
	SenseIngressAddress string `json:"sense-ingress-address"` // IP address of the Sixgill Sense Ingress API server
	SenseIngressAPIKey  string `json:"sense-ingress-api-key"` // API key for Sixgill Sense Ingress API server
}

func main() {

	var err error

	user, err := user.Current()
	if err != nil {
		log.Println("unable to get current user's home directory:", err.Error())
		os.Exit(1)
	}
	userHomeDir := user.HomeDir
	jwtFileName := userHomeDir + "/.sense/ble-client-jwt"
	configFileName := userHomeDir + "/.sense/ble-client-conf.json"
	// get configuration values from config file
	config, err := GetConfig(configFileName)
	if err != nil {
		log.Println("unable to read configuration file `"+configFileName+"`:", err.Error())
		os.Exit(1)
	}
	fmt.Println(config)

	senseIngressAddress = &config.SenseIngressAddress

	// senseIngressAddress = flag.String("sense-ingress-address", "", "IP address of the Sixgill Sense Ingress API server")
	// senseIngressAPIKey := flag.String("sense-ingress-api-key", "", "API key for Sixgill Sense Ingress API server")
	forceRegister := flag.Bool("force-register", false, "force registration (to update a bad JWT, for instance), then quit")
	flag.Parse()

	// if no sense-ingress-address specified
	if len(config.SenseIngressAddress) == 0 {
		log.Println("no Sixgill Sense Ingress API server address specified (sense-ingress-address)")
		os.Exit(1)
	}

	// if no sense-ingress-api-key specified
	if len(config.SenseIngressAPIKey) == 0 {
		log.Println("no Sixgill Sense Ingress API key specified (sense-ingress-api-key)")
		os.Exit(1)
	}

	// fetch any existing JWT
	jwt, err = GetJwtFromFile(jwtFileName)
	if *forceRegister || err != nil {
		log.Println("doing registration (no jwt file present or -force-register specified)")
		// do registration
		url := config.SenseIngressAddress + "/v1/registration"
		statusCode, registrationResponse, err := DoRegistration(url, config.SenseIngressAPIKey)
		if err != nil {
			log.Println("unable to do registration (with error): " + err.Error())
			os.Exit(1)
		}
		if statusCode != 200 {
			log.Println("unable to do registration (with status code): ", statusCode)
			os.Exit(1)
		}

		jwt = registrationResponse.Token

		// save JWT for next time
		err = PutJwtToFile(jwt, jwtFileName)
		if err != nil {
			log.Println("unable to write JWT: " + err.Error())
		}

		os.Exit(0)
	} else {
		log.Println("got jwt from file")
	}

	log.Println("setting up ble listener")
	device, err := dev.DefaultDevice()
	if err != nil {
		log.Println("error setting up ble listener:", err)
		os.Exit(1)
	}
	ble.SetDefaultDevice(device)

	// scan until interrupted
	log.Println("scanning ble")
	ctx := ble.WithSigHandler(context.WithCancel(context.Background()))
	chkErr(ble.Scan(ctx, dup, advHandler, nil))
	log.Println("done scanning ble")

	// recognize stop-related signals
	exitSignal := make(chan os.Signal)
	signal.Notify(exitSignal, syscall.SIGINT, syscall.SIGTERM)

	// wait / block on those signals
	<-exitSignal

	// indicate cleanup
	log.Println("cleaning up")

	// free and clear!
	log.Println("bye")
}

func advHandler(a ble.Advertisement) {

	if len(a.ManufacturerData()) > 0 {
		if a.LocalName() == pattern.Name && reflect.DeepEqual(a.Services(), pattern.Svcs) {

			payload, err := ExtractTemp(a)
			if err != nil {
				log.Println(err.Error())
			}

			start := time.Now()

			// send payload to sixgill Sense Ingress API server
			statusCode, err := PostEvent(*senseIngressAddress+"/v1/iot/events", jwt, payload)
			if err != nil {
				log.Println(err.Error())
			}
			log.Printf("MSG: %s STATUSCODE: %d Duration: %s\n", payload, statusCode, time.Since(start))

			payload, err = ExtractHumidity(a)
			if err != nil {
				log.Println(err.Error())
			}

			start = time.Now()

			// send payload to sixgill Sense Ingress API server
			statusCode, err = PostEvent(*senseIngressAddress+"/v1/iot/events", jwt, payload)
			if err != nil {
				log.Println(err.Error())
			}
			log.Printf("MSG: %s STATUSCODE: %d Duration: %s\n", payload, statusCode, time.Since(start))

		}
	}
}

// ExtractTemp gets the temperature from the sensor result and makes a payload that may be sent to Sense 2.0
func ExtractTemp(a ble.Advertisement) ([]byte, error) {

	buf := bytes.NewReader(a.ManufacturerData())
	hTD := HumiTempData{}
	err := binary.Read(buf, binary.LittleEndian, &hTD)
	if err != nil {
		log.Println("error reading mfg data into buffer:", err)
		return []byte{}, err
	}

	bleData := &BleData{
		Rssi:        a.RSSI(),
		Timestamp:   iso8601.Time(time.Now().UTC()), // use current time for timestamp
		ID:          "Temperature",
		NodeID:      byteAddressToString(hTD.Address[:]),
		SensorValue: float64(hTD.TemperatureWhole) + float64(hTD.TemperatureFraction)/10.0,
		Units:       string(temperatureUnit[0]),
	}
	if hTD.TemperatureSign != 0 {
		bleData.SensorValue *= -1.
	}

	payload, err := json.Marshal(bleData)
	if err != nil {
		return []byte{}, err
	}

	return payload, nil
}

// ExtractHumidity gets the humidity from the sensor result and makes a payload that may be sent to Sense 2.0
func ExtractHumidity(a ble.Advertisement) ([]byte, error) {

	buf := bytes.NewReader(a.ManufacturerData())
	hTD := HumiTempData{}
	err := binary.Read(buf, binary.LittleEndian, &hTD)
	if err != nil {
		log.Println("error reading mfg data into buffer:", err)
		return []byte{}, err
	}

	bleData := &BleData{
		Rssi:        a.RSSI(),
		Timestamp:   iso8601.Time(time.Now().UTC()), // use current time for timestamp
		ID:          "Humidity",
		NodeID:      byteAddressToString(hTD.Address[:]),
		SensorValue: float64(hTD.HumidityWhole) + float64(hTD.HumidityFraction)/10.0,
		Units:       "% RH",
	}
	if hTD.HumiditySign != 0 {
		bleData.SensorValue *= -1.
	}

	payload, err := json.Marshal(bleData)
	if err != nil {
		return []byte{}, err
	}

	return payload, nil
}

func chkErr(err error) {
	switch errors.Cause(err) {
	case nil:
	case context.DeadlineExceeded:
		log.Println("done")
	case context.Canceled:
		log.Println("scanning canceled")
	default:
		log.Println(err.Error())
	}
}

func byteAddressToString(b []byte) string {

	if len(b) != 6 {
		return "bad size"
	}

	return fmt.Sprintf("%X:%X:%X:%X:%X:%X",
		b[0],
		b[1],
		b[2],
		b[3],
		b[4],
		b[5],
	)

}

// DoRegistration registers this application with the Sixgill Sense API server
func DoRegistration(url, apiKey string) (int, pb.RegistrationResponse, error) {

	request := &pb.RegistrationRequest{
		ApiKey: apiKey,
		Properties: &pb.Property{
			Timestamp:       int64(time.Now().UTC().Second()),
			Manufacturer:    "Intel",
			Model:           "Advantech",
			Os:              "wrlinux",
			OsVersion:       "7.0.0.13",
			SoftwareVersion: "sense-ble-client-v0.1",
			Type:            "wrlinux",
			Sensors:         []string{"temperature", "humidity"},
		},
	}

	response := &pb.RegistrationResponse{}
	resp, err := resty.R().
		SetHeader("Content-Type", "application/json").
		SetBody(request).
		SetResult(response).
		SetContentLength(true).
		Post(url)

	return resp.StatusCode(), *response, err
}

// GetJwtFromFile gets the previously stored JWT from the file
func GetJwtFromFile(jwtFile string) (string, error) {
	jwt, err := ioutil.ReadFile(jwtFile)
	log.Println("read from jwt file")
	return string(jwt), err
}

// PutJwtToFile puts the JWT into the file
func PutJwtToFile(jwt, jwtFileName string) error {
	log.Println("writing to jwt file")
	return ioutil.WriteFile(jwtFileName, []byte(jwt), 0644)
}

// PostEvent POSTs the event to the ingress API server using the jwt
func PostEvent(url, jwt string, event []byte) (int, error) {

	request := string(event)
	response := &pb.RegistrationResponse{}
	resp, err := resty.R().
		SetHeader("Content-Type", "application/json").
		SetAuthToken(jwt).
		SetBody(request).
		SetResult(response).
		SetContentLength(true).
		Post(url)

	return resp.StatusCode(), err
}

// GetConfig gets the configuration parameters from the specified json file
func GetConfig(fileName string) (Config, error) {

	config := Config{}

	file, err := os.Open(fileName)
	if err != nil {
		return config, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)

	err = decoder.Decode(&config)

	return config, err
}
