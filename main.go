package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type Point struct {
	T string  `json:"t"`
	D float64 `json:"d"`
}

// --- CONFIGURATION ---
var (
	logs     = true
	phone    = "#######"                // target phone with country code (NO +)
	msgID    = "3ACF29A7FDEB43669569FF" // hex string for ghost reaction
	sMap     sync.Map
	data     []Point
	mtx      sync.Mutex
	maxDelay float64 = 0
)

func handler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Receipt:
		if logs {
			fmt.Printf("[LOG] Receipt Received | Type: %v | ID: %v\n", v.Type, v.MessageIDs)
		}

		if string(v.Type) != "inactive" {
			return
		}

		for _, id := range v.MessageIDs {
			sid := string(id)
			if start, ok := sMap.Load(sid); ok {
				diff := float64(time.Since(start.(time.Time)).Milliseconds())
				now := time.Now().Format("15:04:05")

				mtx.Lock()
				if len(data) > 5 && diff > (maxDelay*2) {
					if logs {
						fmt.Printf("!! OUTLIER IGNORED: %.0f ms (Current Max: %.0f ms)\n", diff, maxDelay)
					}
					mtx.Unlock()
					continue
				}

				if diff > maxDelay {
					maxDelay = diff
				}

				if logs {
					fmt.Printf(">> GRAPH DATA UPDATED (Inactive): %s | Delay: %.0f ms\n", sid, diff)
				}

				data = append(data, Point{T: now, D: diff})
				if len(data) > 60 {
					data = data[1:]
				}
				mtx.Unlock()
			}
		}
	}
}

func serve() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(ui))
	})
	http.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mtx.Lock()
		json.NewEncoder(w).Encode(data)
		mtx.Unlock()
	})
	u := "http://localhost:8080"
	fmt.Printf("\n[WEB] Connecting to %s...\n", u)

	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		exec.Command("open", u).Start()
	default:
		exec.Command("xdg-open", u).Start()
	}
	http.ListenAndServe(":8080", nil)
}

func main() {
	l := waLog.Stdout("DB", "INFO", true)
	ctx := context.Background()
	con, _ := sqlstore.New(ctx, "sqlite3", "file:data.db?_foreign_keys=on", l)
	dev, _ := con.GetFirstDevice(ctx)

	cli := whatsmeow.NewClient(dev, waLog.Stdout("CLI", "INFO", true))
	cli.AddEventHandler(handler)

	if cli.Store.ID == nil {
		ch, _ := cli.GetQRChannel(ctx)
		cli.Connect()
		for e := range ch {
			if e.Event == "code" {
				fmt.Println("Scan the QR code below to log in:")
				qrterminal.GenerateHalfBlock(e.Code, qrterminal.L, os.Stdout)
			}
		}
	} else {
		cli.Connect()
	}

	go serve()

	go func() {
		time.Sleep(5 * time.Second)
		jid, _ := types.ParseJID(phone + "@s.whatsapp.net")
		for {
			req := &waE2E.Message{
				ReactionMessage: &waE2E.ReactionMessage{
					Key: &waCommon.MessageKey{
						RemoteJID: proto.String(jid.String()),
						FromMe:    proto.Bool(false),
						ID:        proto.String(msgID),
					},
					Text:              proto.String("👍"),
					GroupingKey:       proto.String("👍"),
					SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
				},
			}
			res, err := cli.SendMessage(ctx, jid, req)
			if err == nil {
				sMap.Store(res.ID, time.Now())
				if logs {
					fmt.Printf("[SENT] Reaction MESSAGE: %s | ID Reaction: %s\n", msgID, res.ID)
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	cli.Disconnect()
}

const ui = `
<!DOCTYPE html>
<html>
<head>
    <title>Tracker</title>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        body { background-color: #1e1e2f; color: #fff; font-family: sans-serif; text-align: center; margin-top: 50px; }
        .container { width: 85%; margin: auto; background: #2b2b40; padding: 20px; border-radius: 10px; box-shadow: 0 4px 15px rgba(0,0,0,0.5); }
    </style>
</head>
<body>
    <h2>Live Delay Tracker</h2>
    <div class="container">
        <canvas id="c"></canvas>
    </div>
    <script>
        const ctx = document.getElementById('c').getContext('2d');
        const chart = new Chart(ctx, {
            type: 'line',
            data: { labels: [], datasets: [{ 
                label: 'Delay (ms)', 
                data: [], 
                borderColor: '#00ffcc', 
                backgroundColor: 'rgba(0, 255, 204, 0.1)', 
                fill: true,
                borderWidth: 2,
                pointRadius: 3,
                tension: 0.3 
            }] },
            options: {
                responsive: true,
                scales: {
                    y: { 
                        min: -2000, 
                        grid: { color: '#444' }, 
                        ticks: { color: '#aaa' },
                        title: { display: true, text: 'Milliseconds', color: '#fff' }
                    },
                    x: { grid: { color: '#444' }, ticks: { color: '#aaa' } }
                },
                plugins: { legend: { labels: { color: '#fff' } } }
            }
        });
        setInterval(() => {
            fetch('/data').then(r => r.json()).then(d => {
                if (d && d.length > 0) {
                    chart.data.labels = d.map(p => p.t);
                    chart.data.datasets[0].data = d.map(p => p.d);
                    chart.update();
                }
            });
        }, 1000);
    </script>
</body>
</html>`
