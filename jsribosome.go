// Copyright (C) 2013-2017, The MetaCurrency Project (Eric Harris-Braun, Arthur Brock, et. al.)
// Use of this source code is governed by GPLv3 found in the LICENSE file
//----------------------------------------------------------------------------------------
// JSRibosome implements a javascript use of the Ribosome interface

package holochain

import (
	"encoding/json"
	"errors"
	"fmt"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/robertkrimen/otto"
	"strings"
	"time"
)

const (
	JSRibosomeType = "js"
)

// JSRibosome holds data needed for the Javascript VM
type JSRibosome struct {
	zome       *Zome
	vm         *otto.Otto
	lastResult *otto.Value
}

// Type returns the string value under which this ribosome is registered
func (jsr *JSRibosome) Type() string { return JSRibosomeType }

// ChainGenesis runs the application genesis function
// this function gets called after the genesis entries are added to the chain
func (jsr *JSRibosome) ChainGenesis() (err error) {
	v, err := jsr.vm.Run(`genesis()`)
	if err != nil {
		err = fmt.Errorf("Error executing genesis: %v", err)
		return
	}
	if v.IsBoolean() {
		var b bool
		b, err = v.ToBoolean()
		if err != nil {
			return
		}
		if !b {
			err = fmt.Errorf("genesis failed")
		}

	} else {
		err = fmt.Errorf("genesis should return boolean, got: %v", v)
	}
	return
}

// Receive calls the app receive function for node-to-node messages
func (jsr *JSRibosome) Receive(from string, msg string) (response string, err error) {
	var code string
	fnName := "receive"

	code = fmt.Sprintf(`JSON.stringify(%s("%s",JSON.parse("%s")))`, fnName, from, jsSanitizeString(msg))
	Debug(code)
	var v otto.Value
	v, err = jsr.vm.Run(code)
	if err != nil {
		err = fmt.Errorf("Error executing %s: %v", fnName, err)
		return
	}
	response, err = v.ToString()
	return
}

// ValidatePackagingRequest calls the app for a validation packaging request for an action
func (jsr *JSRibosome) ValidatePackagingRequest(action ValidatingAction, def *EntryDef) (req PackagingReq, err error) {
	var code string
	fnName := "validate" + strings.Title(action.Name()) + "Pkg"
	code = fmt.Sprintf(`%s("%s")`, fnName, def.Name)
	Debug(code)
	var v otto.Value
	v, err = jsr.vm.Run(code)
	if err != nil {
		err = fmt.Errorf("Error executing %s: %v", fnName, err)
		return
	}
	if v.IsObject() {
		var m interface{}
		m, err = v.Export()
		if err != nil {
			return
		}
		req = m.(map[string]interface{})
	} else if v.IsNull() {

	} else {
		err = fmt.Errorf("%s should return null or object, got: %v", fnName, v)
	}
	return
}

func prepareJSEntryArgs(def *EntryDef, entry Entry, header *Header) (args string, err error) {
	entryStr := entry.Content().(string)
	switch def.DataFormat {
	case DataFormatRawJS:
		args = entryStr
	case DataFormatString:
		args = "\"" + jsSanitizeString(entryStr) + "\""
	case DataFormatLinks:
		fallthrough
	case DataFormatJSON:
		args = fmt.Sprintf(`JSON.parse("%s")`, jsSanitizeString(entryStr))
	default:
		err = errors.New("data format not implemented: " + def.DataFormat)
		return
	}
	var hdr string
	if header != nil {
		hdr = fmt.Sprintf(
			`{"EntryLink":"%s","Type":"%s","Time":"%s"}`,
			header.EntryLink.String(),
			header.Type,
			header.Time.UTC().Format(time.RFC3339),
		)
	} else {
		hdr = `{"EntryLink":"","Type":"","Time":""}`
	}
	args += "," + hdr
	return
}

func prepareJSValidateArgs(action Action, def *EntryDef) (args string, err error) {
	switch t := action.(type) {
	case *ActionPut:
		args, err = prepareJSEntryArgs(def, t.entry, t.header)
	case *ActionCommit:
		args, err = prepareJSEntryArgs(def, t.entry, t.header)
	case *ActionMod:
		args, err = prepareJSEntryArgs(def, t.entry, t.header)
		if err == nil {
			args += fmt.Sprintf(`,"%s"`, t.replaces.String())
		}
	case *ActionDel:
		args = fmt.Sprintf(`"%s"`, t.entry.Hash.String())
	case *ActionLink:
		var j []byte
		j, err = json.Marshal(t.links)
		if err == nil {
			args = fmt.Sprintf(`"%s",JSON.parse("%s")`, t.validationBase.String(), jsSanitizeString(string(j)))
		}
	default:
		err = fmt.Errorf("can't prepare args for %T: ", t)
		return
	}
	return
}

func buildJSValidateAction(action Action, def *EntryDef, pkg *ValidationPackage, sources []string) (code string, err error) {
	fnName := "validate" + strings.Title(action.Name())
	var args string
	args, err = prepareJSValidateArgs(action, def)
	if err != nil {
		return
	}
	srcs := mkJSSources(sources)

	var pkgObj string
	if pkg == nil || pkg.Chain == nil {
		pkgObj = "{}"
	} else {
		var j []byte
		j, err = json.Marshal(pkg.Chain)
		if err != nil {
			return
		}
		pkgObj = fmt.Sprintf(`{"Chain":%s}`, j)
	}
	code = fmt.Sprintf(`%s("%s",%s,%s,%s)`, fnName, def.Name, args, pkgObj, srcs)

	return
}

// ValidateAction builds the correct validation function based on the action an calls it
func (jsr *JSRibosome) ValidateAction(action Action, def *EntryDef, pkg *ValidationPackage, sources []string) (err error) {
	var code string
	code, err = buildJSValidateAction(action, def, pkg, sources)
	if err != nil {
		return
	}
	Debug(code)
	err = jsr.runValidate(action.Name(), code)
	return
}

func mkJSSources(sources []string) (srcs string) {
	srcs = `["` + strings.Join(sources, `","`) + `"]`
	return
}

func (jsr *JSRibosome) prepareJSValidateEntryArgs(def *EntryDef, entry Entry, sources []string) (e string, srcs string, err error) {
	c := entry.Content().(string)
	switch def.DataFormat {
	case DataFormatRawJS:
		e = c
	case DataFormatString:
		e = "\"" + jsSanitizeString(c) + "\""
	case DataFormatLinks:
		fallthrough
	case DataFormatJSON:
		e = fmt.Sprintf(`JSON.parse("%s")`, jsSanitizeString(c))
	default:
		err = errors.New("data format not implemented: " + def.DataFormat)
		return
	}
	srcs = mkJSSources(sources)
	return
}

func (jsr *JSRibosome) runValidate(fnName string, code string) (err error) {
	var v otto.Value
	v, err = jsr.vm.Run(code)
	if err != nil {
		err = fmt.Errorf("Error executing %s: %v", fnName, err)
		return
	}
	if v.IsBoolean() {
		if v.IsBoolean() {
			var b bool
			b, err = v.ToBoolean()
			if err != nil {
				return
			}
			if !b {
				err = ValidationFailedErr
			}
		}
	} else {
		err = fmt.Errorf("%s should return boolean, got: %v", fnName, v)
	}
	return
}

func (jsr *JSRibosome) validateEntry(fnName string, def *EntryDef, entry Entry, header *Header, sources []string) (err error) {

	e, srcs, err := jsr.prepareJSValidateEntryArgs(def, entry, sources)
	if err != nil {
		return
	}

	hdr := fmt.Sprintf(
		`{"EntryLink":"%s","Type":"%s","Time":"%s"}`,
		header.EntryLink.String(),
		header.Type,
		header.Time.UTC().Format(time.RFC3339),
	)

	code := fmt.Sprintf(`%s("%s",%s,%s,%s)`, fnName, def.Name, e, hdr, srcs)
	Debugf("%s: %s", fnName, code)
	err = jsr.runValidate(fnName, code)
	if err != nil && err == ValidationFailedErr {
		err = fmt.Errorf("Invalid entry: %v", entry.Content())
	}

	return
}

const (
	JSLibrary = `var HC={Version:` + `"` + VersionStr + "\"" +
		`,Status:{Live:` + StatusLiveVal +
		`,Rejected:` + StatusRejectedVal +
		`,Deleted:` + StatusDeletedVal +
		`,Modified:` + StatusModifiedVal +
		`,Any:` + StatusAnyVal +
		"}" +
		`,GetMask:{Default:` + GetMaskDefaultStr +
		`,Entry:` + GetMaskEntryStr +
		`,EntryType:` + GetMaskEntryTypeStr +
		`,Sources:` + GetMaskSourcesStr +
		`,All:` + GetMaskAllStr +
		"}" +
		`,LinkAction:{Add:"` + AddAction + `",Del:"` + DelAction + `"}` +
		`,PkgReq:{Chain:"` + PkgReqChain + `"` +
		`,ChainOpt:{None:` + PkgReqChainOptNoneStr +
		`,Headers:` + PkgReqChainOptHeadersStr +
		`,Entries:` + PkgReqChainOptEntriesStr +
		`,Full:` + PkgReqChainOptFullStr +
		"}" +
		"}" +
		`};`
)

// jsSanatizeString makes sure all quotes are quoted and returns are removed
func jsSanitizeString(s string) string {
	s0 := strings.Replace(s, "\n", "", -1)
	s1 := strings.Replace(s0, "\r", "", -1)
	s2 := strings.Replace(s1, "\"", "\\\"", -1)
	return s2
}

// Call calls the zygo function that was registered with expose
func (jsr *JSRibosome) Call(fn *FunctionDef, params interface{}) (result interface{}, err error) {
	var code string
	switch fn.CallingType {
	case STRING_CALLING:
		code = fmt.Sprintf(`%s("%s");`, fn.Name, jsSanitizeString(params.(string)))
	case JSON_CALLING:
		if params.(string) == "" {
			code = fmt.Sprintf(`JSON.stringify(%s());`, fn.Name)
		} else {
			p := jsSanitizeString(params.(string))
			code = fmt.Sprintf(`JSON.stringify(%s(JSON.parse("%s")));`, fn.Name, p)
		}
	default:
		err = errors.New("params type not implemented")
		return
	}
	Debugf("JS Call: %s", code)
	var v otto.Value
	v, err = jsr.vm.Run(code)
	if err == nil {
		if v.IsObject() && v.Class() == "Error" {
			Debugf("JS Error:\n%v", v)
			var message otto.Value
			message, err = v.Object().Get("message")
			if err == nil {
				err = errors.New(message.String())
			}
		} else {
			result, err = v.ToString()
		}
	}
	return
}

// jsProcessArgs processes oArgs according to the args spec filling args[].value with the converted value
func jsProcessArgs(jsr *JSRibosome, args []Arg, oArgs []otto.Value) (err error) {
	err = checkArgCount(args, len(oArgs))
	if err != nil {
		return err
	}

	// check arg types
	for i, arg := range oArgs {
		switch args[i].Type {
		case StringArg:
			if arg.IsString() {
				args[i].value, _ = arg.ToString()
			} else {
				return argErr("string", i+1, args[i])
			}
		case HashArg:
			if arg.IsString() {
				str, _ := arg.ToString()
				var hash Hash
				hash, err = NewHash(str)
				if err != nil {
					return
				}
				args[i].value = hash
			} else {
				return argErr("string", i+1, args[i])
			}
		case IntArg:
			if arg.IsNumber() {
				integer, err := arg.ToInteger()
				if err != nil {
					return err
				}
				args[i].value = integer
			} else {
				return argErr("int", i+1, args[i])
			}
		case BoolArg:
			if arg.IsBoolean() {
				boolean, err := arg.ToBoolean()
				if err != nil {
					return err
				}
				args[i].value = boolean
			} else {
				return argErr("boolean", i+1, args[i])
			}
		case ArgsArg:
			if arg.IsString() {
				str, err := arg.ToString()
				if err != nil {
					return err
				}
				args[i].value = str
			} else if arg.IsObject() {
				v, err := jsr.vm.Call("JSON.stringify", nil, arg)
				if err != nil {
					return err
				}
				entry, err := v.ToString()
				if err != nil {
					return err
				}
				args[i].value = entry

			} else {
				return argErr("string or object", i+1, args[i])
			}
		case EntryArg:
			if arg.IsString() {
				str, err := arg.ToString()
				if err != nil {
					return err
				}
				args[i].value = str
			} else if arg.IsObject() {
				v, err := jsr.vm.Call("JSON.stringify", nil, arg)
				if err != nil {
					return err
				}
				entry, err := v.ToString()
				if err != nil {
					return err
				}
				args[i].value = entry

			} else {
				return argErr("string or object", i+1, args[i])
			}
		case MapArg:
			if arg.IsObject() {
				m, err := arg.Export()
				if err != nil {
					return err
				}
				args[i].value = m
			} else {
				return argErr("object", i+1, args[i])
			}
		case ToStrArg:
			var str string
			if arg.IsObject() {
				v, err := jsr.vm.Call("JSON.stringify", nil, arg)
				if err != nil {
					return err
				}
				str, err = v.ToString()
				if err != nil {
					return err
				}
			} else {
				str, _ = arg.ToString()
			}
			args[i].value = str
		}
	}
	return
}

func mkOttoErr(jsr *JSRibosome, msg string) otto.Value {
	return jsr.vm.MakeCustomError("HolochainError", msg)
}

func numInterfaceToInt(num interface{}) (val int, ok bool) {
	ok = true
	switch t := num.(type) {
	case int64:
		val = int(t)
	case float64:
		val = int(t)
	case int:
		val = t
	default:
		ok = false
	}
	return
}

// NewJSRibosome factory function to build a javascript execution environment for a zome
func NewJSRibosome(h *Holochain, zome *Zome) (n Ribosome, err error) {
	jsr := JSRibosome{
		zome: zome,
		vm:   otto.New(),
	}

	err = jsr.vm.Set("property", func(call otto.FunctionCall) otto.Value {
		a := &ActionProperty{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}

		a.prop = args[0].value.(string)

		var p interface{}
		p, err = a.Do(h)
		if err != nil {
			return otto.UndefinedValue()
		}
		result, _ := jsr.vm.ToValue(p)
		return result
	})
	if err != nil {
		return nil, err
	}

	err = jsr.vm.Set("debug", func(call otto.FunctionCall) otto.Value {
		a := &ActionDebug{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		a.msg = args[0].value.(string)
		a.Do(h)
		return otto.UndefinedValue()
	})

	err = jsr.vm.Set("makeHash", func(call otto.FunctionCall) otto.Value {
		a := &ActionMakeHash{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}

		a.entry = &GobEntry{C: args[0].value.(string)}
		var r interface{}
		r, err = a.Do(h)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		var entryHash Hash
		if r != nil {
			entryHash = r.(Hash)
		}
		result, _ := jsr.vm.ToValue(entryHash.String())
		return result
	})

	err = jsr.vm.Set("send", func(call otto.FunctionCall) otto.Value {
		a := &ActionSend{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}

		a.to, err = peer.IDB58Decode(args[0].value.(Hash).String())
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		msg := args[1].value.(map[string]interface{})
		var j []byte
		j, err = json.Marshal(msg)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}

		a.msg.ZomeType = jsr.zome.Name
		a.msg.Body = string(j)

		var r interface{}
		r, err = a.Do(h)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		var result otto.Value
		result, err = jsr.vm.ToValue(r)

		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		return result
	})

	err = jsr.vm.Set("call", func(call otto.FunctionCall) otto.Value {
		a := &ActionCall{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		a.zome = args[0].value.(string)
		var zome *Zome
		zome, err = h.GetZome(a.zome)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		a.function = args[1].value.(string)
		var fn *FunctionDef
		fn, err = zome.GetFunctionDef(a.function)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		if fn.CallingType == JSON_CALLING {
			if !call.ArgumentList[2].IsObject() {
				return mkOttoErr(&jsr, "function calling type requires object argument type")
			}
		}
		a.args = args[2].value.(string)

		var r interface{}
		r, err = a.Do(h)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		var result otto.Value
		result, err = jsr.vm.ToValue(r)

		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		return result
	})

	err = jsr.vm.Set("commit", func(call otto.FunctionCall) otto.Value {
		var a Action = &ActionCommit{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}

		entryType := args[0].value.(string)
		entryStr := args[1].value.(string)
		var r interface{}
		entry := GobEntry{C: entryStr}
		r, err = NewCommitAction(entryType, &entry).Do(h)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		var entryHash Hash
		if r != nil {
			entryHash = r.(Hash)
		}

		result, _ := jsr.vm.ToValue(entryHash.String())
		return result
	})
	if err != nil {
		return nil, err
	}
	err = jsr.vm.Set("get", func(call otto.FunctionCall) (result otto.Value) {
		var a Action = &ActionGet{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}

		options := GetOptions{StatusMask: StatusDefault}
		if len(call.ArgumentList) == 2 {
			opts := args[1].value.(map[string]interface{})
			mask, ok := opts["StatusMask"]
			if ok {
				// otto returns int64 or float64 depending on whether
				// the mask was returned by constant or addition so
				maskval, ok := numInterfaceToInt(mask)
				if !ok {
					return mkOttoErr(&jsr, fmt.Sprintf("expecting int StatusMask attribute, got %T", mask))
				}
				options.StatusMask = int(maskval)
			}
			mask, ok = opts["GetMask"]
			if ok {
				maskval, ok := numInterfaceToInt(mask)
				if !ok {

					return mkOttoErr(&jsr, fmt.Sprintf("expecting int GetMask attribute, got %T", mask))
				}
				options.GetMask = int(maskval)
			}
			local, ok := opts["Local"]
			if ok {
				options.Local = local.(bool)
			}
		}
		req := GetReq{H: args[0].value.(Hash), StatusMask: options.StatusMask, GetMask: options.GetMask}
		var r interface{}
		r, err = NewGetAction(req, &options).Do(h)
		mask := options.GetMask
		if mask == GetMaskDefault {
			mask = GetMaskEntry
		}
		if err == nil {
			getResp := r.(GetResp)
			var singleValueReturn bool
			if mask&GetMaskEntry != 0 {
				if GetMaskEntry == mask {
					singleValueReturn = true
					result, err = jsr.vm.ToValue(getResp.Entry)
				}
			}
			if mask&GetMaskEntryType != 0 {
				if GetMaskEntryType == mask {
					singleValueReturn = true
					result, err = jsr.vm.ToValue(getResp.EntryType)
				}
			}
			if mask&GetMaskSources != 0 {
				if GetMaskSources == mask {
					singleValueReturn = true
					result, err = jsr.vm.ToValue(getResp.Sources)
				}
			}
			if err == nil && !singleValueReturn {
				respObj := make(map[string]interface{})
				if mask&GetMaskEntry != 0 {
					respObj["Entry"] = getResp.Entry
				}
				if mask&GetMaskEntryType != 0 {
					respObj["EntryType"] = getResp.EntryType
				}
				if mask&GetMaskSources != 0 {
					respObj["Sources"] = getResp.Sources
				}
				result, err = jsr.vm.ToValue(respObj)
			}
			return
		}

		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		panic("Shouldn't get here!")
	})
	if err != nil {
		return nil, err
	}

	err = jsr.vm.Set("update", func(call otto.FunctionCall) (result otto.Value) {
		var a Action = &ActionMod{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		entryType := args[0].value.(string)
		entryStr := args[1].value.(string)
		replaces := args[2].value.(Hash)

		entry := GobEntry{C: entryStr}
		resp, err := NewModAction(entryType, &entry, replaces).Do(h)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		var entryHash Hash
		if resp != nil {
			entryHash = resp.(Hash)
		}
		result, _ = jsr.vm.ToValue(entryHash.String())

		return

	})
	if err != nil {
		return nil, err
	}

	err = jsr.vm.Set("remove", func(call otto.FunctionCall) (result otto.Value) {
		var a Action = &ActionDel{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return mkOttoErr(&jsr, err.Error())
		}
		entry := DelEntry{
			Hash:    args[0].value.(Hash),
			Message: args[1].value.(string),
		}
		header, err := h.chain.GetEntryHeader(entry.Hash)
		if err == nil {
			var resp interface{}
			resp, err = NewDelAction(header.Type, entry).Do(h)
			if err == nil {
				var entryHash Hash
				if resp != nil {
					entryHash = resp.(Hash)
				}
				result, _ = jsr.vm.ToValue(entryHash.String())
				return
			}
		}
		result = mkOttoErr(&jsr, err.Error())
		return

	})
	if err != nil {
		return nil, err
	}

	err = jsr.vm.Set("getLink", func(call otto.FunctionCall) (result otto.Value) {
		var a Action = &ActionGetLink{}
		args := a.Args()
		err := jsProcessArgs(&jsr, args, call.ArgumentList)
		if err != nil {
			return jsr.vm.MakeCustomError("HolochainError", err.Error())
		}
		base := args[0].value.(Hash)
		tag := args[1].value.(string)

		l := len(call.ArgumentList)
		options := GetLinkOptions{Load: false, StatusMask: StatusLive}
		if l == 3 {
			opts := args[2].value.(map[string]interface{})
			load, ok := opts["Load"]
			if ok {
				loadval, ok := load.(bool)
				if !ok {
					return mkOttoErr(&jsr, fmt.Sprintf("expecting boolean Load attribute in object, got %T", load))
				}
				options.Load = loadval
			}
			mask, ok := opts["StatusMask"]
			if ok {
				maskval, ok := numInterfaceToInt(mask)
				if !ok {
					return mkOttoErr(&jsr, fmt.Sprintf("expecting int StatusMask attribute in object, got %T", mask))
				}
				options.StatusMask = int(maskval)
			}
		}
		var response interface{}

		response, err = NewGetLinkAction(&LinkQuery{Base: base, T: tag, StatusMask: options.StatusMask}, &options).Do(h)
		Debugf("RESPONSE:%v\n", response)

		if err == nil {
			result, err = jsr.vm.ToValue(response)
		} else {
			result = mkOttoErr(&jsr, err.Error())
		}

		return
	})
	if err != nil {
		return nil, err
	}

	l := JSLibrary
	if h != nil {
		l += fmt.Sprintf(`var App = {Name:"%s",DNA:{Hash:"%s"},Agent:{Hash:"%s",String:"%s"},Key:{Hash:"%s"}};`, h.nucleus.dna.Name, h.dnaHash, h.agentHash, h.Agent().Name(), h.nodeIDStr)
	}
	_, err = jsr.Run(l + zome.Code)
	if err != nil {
		return
	}
	n = &jsr
	return
}

// Run executes javascript code
func (jsr *JSRibosome) Run(code string) (result interface{}, err error) {
	v, err := jsr.vm.Run(code)
	if err != nil {
		err = errors.New("JS exec error: " + err.Error())
		return
	}
	jsr.lastResult = &v
	result = &v
	return
}
