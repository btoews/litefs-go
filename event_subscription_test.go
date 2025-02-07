package litefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func ExampleSubscribeEvents() {
	// setup fake events endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"type":"init","data":{"isPrimary":true,"hostname":"node-1"}}`)
		fmt.Fprintln(w, `{"type":"primaryChange","data":{"isPrimary":false,"hostname":"node-2"}}`)
	}))
	defer server.Close()
	EventSubscriptionURL = server.URL

	subscriber := SubscribeEvents()
	defer subscriber.Close()

	for {
		select {
		case event := <-subscriber.C():
			switch data := event.Data.(type) {
			case *InitEventData:
				fmt.Printf("init: isPrimary=%t hostname=%s\n", data.IsPrimary, data.Hostname)
			case *PrimaryChangeEventData:
				fmt.Printf("primary change: isPrimary=%t hostname=%s\n", data.IsPrimary, data.Hostname)
			case *TxEventData:
				fmt.Printf("tx: %s\n", data.TXID)
			}
		case err := <-subscriber.ErrC():
			fmt.Println(err)
			return
		}
	}

	// Output: init: isPrimary=true hostname=node-1
	// primary change: isPrimary=false hostname=node-2
	// EOF
}

func TestEventStream(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		es := mockServerSubscription(t,
			initEventJSON, flush, sleep10,
			txEventJSON, flush, sleep10,
			pChangeNode2EventJSON,
		)

		assertReadEvent(t, es, initEvent)
		assertReadEvent(t, es, txEvent)
		assertReadEvent(t, es, pChangeNode2Event)
	})

	t.Run("error status", func(t *testing.T) {
		es := mockServerSubscription(t,
			status500,
			initEventJSON, flush, sleep10,
		)

		select {
		case <-es.C():
			t.Fatal("expected error")
		case err := <-es.ErrC():
			if !errors.Is(err, errUnexpectedStatus) {
				t.Fatalf("expected errUnexpectedStatus, got %s", err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout")
		}

		assertReadEvent(t, es, initEvent)
	})

	t.Run("premature hangup", func(t *testing.T) {
		es := mockServerSubscription(t,
			initEventJSON, flush, sleep10,
			hangup,
			initEventJSON, flush, sleep10,
		)

		assertReadEvent(t, es, initEvent)

		select {
		case <-es.C():
			t.Fatal("expected error")
		case err := <-es.ErrC():
			if err.Error() != "unexpected EOF" {
				t.Fatalf("expected errUnexpectedStatus, got %s", err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout")
		}

		assertReadEvent(t, es, initEvent)
	})

	t.Run("bad response", func(t *testing.T) {
		es := mockServerSubscription(t,
			"beep boop", flush, sleep10,
			initEventJSON, flush, sleep10,
		)

		select {
		case <-es.C():
			t.Fatal("expected error")
		case err := <-es.ErrC():
			jerr := new(json.SyntaxError)
			if !errors.As(err, &jerr) {
				t.Fatalf("expected json.SyntaxError, got %s", err)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout")
		}

		assertReadEvent(t, es, initEvent)
	})
}

const (
	status500             = "status500"
	hangup                = "hangup"
	sleep10               = "sleep10"
	flush                 = "flush"
	initEventJSON         = `{"type":"init","data":{"isPrimary":true,"hostname":"node-1"}}`
	txEventJSON           = `{"type":"tx","db":"db","data":{"txID":"0000000000000027","postApplyChecksum":"83b05248774ce767","pageSize":4096,"commit":2,"timestamp":"0001-01-01T00:00:00Z"}}`
	pChangeNode2EventJSON = `{"type":"primaryChange","data":{"isPrimary":false,"hostname":"node-2"}}`
	pChangeNode1EventJSON = `{"type":"primaryChange","data":{"isPrimary":true,"hostname":"node-1"}}`
)

var (
	initEvent         = &Event{Type: EventTypeInit, Data: &InitEventData{IsPrimary: true, Hostname: "node-1"}}
	txEvent           = &Event{Type: EventTypeTx, DB: "db", Data: &TxEventData{TXID: "0000000000000027", PostApplyChecksum: "83b05248774ce767", PageSize: 4096, Commit: 2}}
	pChangeNode2Event = &Event{Type: EventTypePrimaryChange, Data: &PrimaryChangeEventData{IsPrimary: false, Hostname: "node-2"}}
	pChangeNode1Event = &Event{Type: EventTypePrimaryChange, Data: &PrimaryChangeEventData{IsPrimary: true, Hostname: "node-1"}}
)

func mockServerSubscription(t *testing.T, resps ...string) *EventSubscription {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for len(resps) != 0 {
			if r.Context().Err() != nil {
				return
			}

			resp := resps[0]
			resps = resps[1:]

			switch resp {
			case status500:
				w.WriteHeader(http.StatusInternalServerError)
				return
			case hangup:
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			case sleep10:
				time.Sleep(10 * time.Millisecond)
			case flush:
				w.(http.Flusher).Flush()
			default:
				fmt.Fprintln(w, resp)
			}
		}
	}))
	t.Cleanup(s.Close)
	EventSubscriptionURL = s.URL

	es := SubscribeEvents()
	t.Cleanup(es.Close)

	return es
}

func assertReadEvent(t *testing.T, es *EventSubscription, expected *Event) {
	t.Helper()

	select {
	case event := <-es.C():
		if !reflect.DeepEqual(event, expected) {
			t.Fatalf("wrong event\nexpected: %#v\nactual:%#v", expected, event)
		}
	case err := <-es.ErrC():
		t.Fatalf("unexpected error: %s", err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout")
	}
}
