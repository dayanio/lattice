package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
)

func main() {
	token := os.Args[1]
	nc, err := nats.Connect("nats://lattice-svc:4222", nats.Timeout(5*time.Second))
	if err != nil { panic(err) }
	defer nc.Close()
	payload, _ := json.Marshal(map[string]string{"appId": "node-a", "token": token})
	raw, err := nc.Request("lattice.signals.peer.GetNetMap", payload, 10*time.Second)
	if err != nil { panic(err) }
	var out map[string]any
	if err := json.Unmarshal(raw.Data, &out); err != nil { panic(err) }
	pretty, _ := json.MarshalIndent(out["computedrules"], "", "  ")
	fmt.Println("version:", out["configVersion"])
	fmt.Println("computedrules:", string(pretty))
}
