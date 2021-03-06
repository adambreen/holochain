// Copyright (C) 2013-2017, The MetaCurrency Project (Eric Harris-Braun, Arthur Brock, et. al.)
// Use of this source code is governed by GPLv3 found in the LICENSE file
//----------------------------------------------------------------------------------------

// implements webserver functionality for holochain UI

package ui

import (
	_ "encoding/json"
	"errors"
	"fmt"
	websocket "github.com/gorilla/websocket"
	holo "github.com/metacurrency/holochain"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
)

type WebServer struct {
	h    *holo.Holochain
	port string
	log  holo.Logger
	errs holo.Logger
}

func NewWebServer(h *holo.Holochain, port string) *WebServer {
	w := WebServer{h: h, port: port}
	w.log = holo.Logger{Format: "%{color:magenta}%{message}"}
	w.errs = holo.Logger{Format: "%{color:red}%{time} %{message}", Enabled: true}
	return &w
}

func (ws *WebServer) Start() {

	ws.log.New(nil)
	ws.errs.New(os.Stderr)

	fs := http.FileServer(http.Dir(ws.h.UIPath()))
	http.Handle("/", fs)

	var upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	http.HandleFunc("/_sock/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			ws.errs.Logf(err.Error())
			return
		}

		for {
			var v map[string]string
			err := conn.ReadJSON(&v)

			ws.log.Logf("conn got: %v\n", v)

			if err != nil {
				ws.errs.Log(err)
				return
			}
			zome := v["zome"]
			function := v["fn"]
			result, err := ws.call(zome, function, v["arg"])
			switch t := result.(type) {
			case string:
				err = conn.WriteMessage(websocket.TextMessage, []byte(t))
			case []byte:
				err = conn.WriteMessage(websocket.TextMessage, t)
				//err = conn.WriteJSON(t)
			default:
				err = fmt.Errorf("Unknown type from Call of %s:%s", zome, function)
			}

			if err != nil {
				ws.errs.Log(err)
				return
			}
		}
	})

	http.HandleFunc("/fn/", func(w http.ResponseWriter, r *http.Request) {

		var err error
		var errCode = 400
		defer func() {
			if err != nil {
				ws.log.Logf("ERROR:%s,code:%d", err.Error(), errCode)
				http.Error(w, err.Error(), errCode)
			}
		}()

		/*		if r.Method == "GET" {
					fmt.Printf("processing Get:%s\n", r.URL.Path)

					http.Redirect(w, r, "/static", http.StatusSeeOther)
				}
		*/
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			errCode, err = mkErr("unable to read body", 500)
			return
		}
		ws.log.Logf("processing req:%s\n  Body:%v\n", r.URL.Path, string(body))

		path := strings.Split(r.URL.Path, "/")

		zome := path[2]
		function := path[3]
		args := string(body)
		result, err := ws.call(zome, function, args)
		if err != nil {
			ws.log.Logf("call of %s:%s resulted in error: %v\n", zome, function, err)
			http.Error(w, err.Error(), 500)

			return
		}
		ws.log.Logf(" result: %v\n", result)
		switch t := result.(type) {
		case string:
			fmt.Fprintf(w, t)
		case []byte:
			fmt.Fprintf(w, string(t))
		default:
			err = fmt.Errorf("Unknown type from Call of %s:%s", zome, function)
		}
	}) // set router
	ws.log.Logf("starting server on localhost:%s\n", ws.port)
	err := http.ListenAndServe(":"+ws.port, nil) // set listen port
	if err != nil {
		ws.errs.Logf("Couldn't start server: %v", err)
	}
}

func mkErr(etext string, code int) (int, error) {
	return code, errors.New(etext)
}

func (ws *WebServer) call(zome string, function string, args string) (result interface{}, err error) {

	ws.log.Logf("calling %s:%s(%s)\n", zome, function, args)
	result, err = ws.h.Call(zome, function, args, holo.PUBLIC_EXPOSURE)

	if err != nil {
		_, err = mkErr(err.Error(), 400)
	}
	return
}
