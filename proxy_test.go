package goproxy_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"image"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/abourget/goproxy"
	"github.com/abourget/goproxy/ext/image"
)

var acceptAllCerts = &tls.Config{InsecureSkipVerify: true}

var noProxyClient = &http.Client{Transport: &http.Transport{TLSClientConfig: acceptAllCerts}}

var https = httptest.NewTLSServer(nil)
var srv = httptest.NewServer(nil)
var fs = httptest.NewServer(http.FileServer(http.Dir(".")))

type QueryHandler struct{}

func (QueryHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		panic(err)
	}
	io.WriteString(w, req.Form.Get("result"))
}

func init() {
	http.DefaultServeMux.Handle("/bobo", ConstantHandler("bobo"))
	http.DefaultServeMux.Handle("/query", QueryHandler{})
}

type ConstantHandler string

func (h ConstantHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, string(h))
}

func get(url string, client *http.Client) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	txt, err := ioutil.ReadAll(resp.Body)
	defer resp.Body.Close()
	if err != nil {
		return nil, err
	}
	return txt, nil
}

func getOrFail(url string, client *http.Client, t *testing.T) []byte {
	txt, err := get(url, client)
	if err != nil {
		t.Fatal("Can't fetch url", url, err)
	}
	return txt
}

func localFile(url string) string { return fs.URL + "/" + url }
func localTls(url string) string  { return https.URL + url }

func TestSimpleHttpReqWithProxy(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true
	client, s := oneShotProxy(proxy, t)
	defer s.Close()

	if r := string(getOrFail(srv.URL+"/bobo", client, t)); r != "bobo" {
		t.Error("proxy server does not serve constant handlers", r)
	}
	if r := string(getOrFail(srv.URL+"/bobo", client, t)); r != "bobo" {
		t.Error("proxy server does not serve constant handlers", r)
	}

	// NOTE: This issues a CONNECT call, because it doesn't default to port 80 ??
	if string(getOrFail(https.URL+"/bobo", client, t)) != "bobo" {
		t.Error("TLS server does not serve constant handlers, when proxy is used")
	}
}

func oneShotProxy(proxy *goproxy.ProxyHttpServer, t *testing.T) (client *http.Client, s *httptest.Server) {
	s = httptest.NewServer(proxy)

	proxyUrl, _ := url.Parse(s.URL)
	tr := &http.Transport{TLSClientConfig: acceptAllCerts, Proxy: http.ProxyURL(proxyUrl)}
	client = &http.Client{Transport: tr}
	return
}

func TestSimpleConditionalHook(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()

	mangleRequestPath := goproxy.HandlerFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		ctx.Req.URL.Path = "/bobo"
		return goproxy.NEXT
	})
	proxy.HandleRequest(goproxy.RemoteAddrIs("127.0.0.1")(mangleRequestPath))
	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	if result := string(getOrFail(srv.URL+("/momo"), client, t)); result != "bobo" {
		t.Error("Redirecting all requests from 127.0.0.1 to bobo, didn't work." +
			" (Might break if Go's client sets RemoteAddr to IPv6 address). Got: " +
			result)
	}
}

func TestAlwaysHook(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleRequestFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		ctx.Req.URL.Path = "/bobo"
		return goproxy.NEXT
	})
	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	if result := string(getOrFail(srv.URL+("/momo"), client, t)); result != "bobo" {
		t.Error("Redirecting all requests from 127.0.0.1 to bobo, didn't work." +
			" (Might break if Go's client sets RemoteAddr to IPv6 address). Got: " +
			result)
	}
}

func TestReplaceReponseForUrl(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleResponse(goproxy.UrlIsIn("/koko")(goproxy.HandlerFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		if ctx.Resp == nil {
			return goproxy.NEXT
		}
		ctx.Resp.StatusCode = http.StatusOK
		ctx.SetResponseBody([]byte("chico"))

		return goproxy.NEXT
	})))

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	if result := string(getOrFail(srv.URL+("/koko"), client, t)); result != "chico" {
		t.Error("hooked 'koko', should be chico, instead:", result)
	}
	if result := string(getOrFail(srv.URL+("/bobo"), client, t)); result != "bobo" {
		t.Error("still, bobo should stay as usual, instead:", result)
	}
}

func TestOneShotFileServer(t *testing.T) {
	client, l := oneShotProxy(goproxy.NewProxyHttpServer(), t)
	defer l.Close()

	file := "test_data/panda.png"
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal("Cannot find", file)
	}
	if resp, err := client.Get(fs.URL + "/" + file); err == nil {
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Fatal("got", string(b))
		}
		if int64(len(b)) != info.Size() {
			t.Error("Expected Length", file, info.Size(), "actually", len(b), "starts", string(b[:10]))
		}
	} else {
		t.Fatal("Cannot read from fs server", err)
	}
}

func TestContentType(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()

	mangleImage := goproxy.HandlerFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		ctx.Resp.Header.Set("X-Shmoopi", "1")
		return goproxy.NEXT
	})
	proxy.HandleResponse(goproxy.RespContentTypeIs("image/png")(mangleImage))

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	for _, file := range []string{"test_data/panda.png", "test_data/football.png"} {
		if resp, err := client.Get(localFile(file)); err != nil || resp.Header.Get("X-Shmoopi") != "1" {
			if err == nil {
				t.Error("pngs should have X-Shmoopi header = 1, actually", resp.Header.Get("X-Shmoopi"))
			} else {
				t.Error("error reading png", err)
			}
		}
	}

	file := "baby.jpg"
	if resp, err := client.Get(localFile(file)); err != nil || resp.Header.Get("X-Shmoopi") != "" {
		if err == nil {
			t.Error("Non png images should NOT have X-Shmoopi header at all", resp.Header.Get("X-Shmoopi"))
		} else {
			t.Error("error reading png", err)
		}
	}
}

func getImage(file string, t *testing.T) image.Image {
	newimage, err := ioutil.ReadFile(file)
	if err != nil {
		t.Fatal("Cannot read file", file, err)
	}
	img, _, err := image.Decode(bytes.NewReader(newimage))
	if err != nil {
		t.Fatal("Cannot decode image", file, err)
	}
	return img
}

func readAll(r io.Reader, t *testing.T) []byte {
	b, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatal("Cannot read", err)
	}
	return b
}
func readFile(file string, t *testing.T) []byte {
	b, err := ioutil.ReadFile(file)
	if err != nil {
		t.Fatal("Cannot read", err)
	}
	return b
}
func fatalOnErr(err error, msg string, t *testing.T) {
	if err != nil {
		t.Fatal(msg, err)
	}
}
func panicOnErr(err error, msg string) {
	if err != nil {
		println(err.Error() + ":-" + msg)
		os.Exit(-1)
	}
}

func compareImage(eImg, aImg image.Image, t *testing.T) {
	if eImg.Bounds().Dx() != aImg.Bounds().Dx() || eImg.Bounds().Dy() != aImg.Bounds().Dy() {
		t.Error("image sizes different")
		return
	}
	for i := 0; i < eImg.Bounds().Dx(); i++ {
		for j := 0; j < eImg.Bounds().Dy(); j++ {
			er, eg, eb, ea := eImg.At(i, j).RGBA()
			ar, ag, ab, aa := aImg.At(i, j).RGBA()
			if er != ar || eg != ag || eb != ab || ea != aa {
				t.Error("images different at", i, j, "vals\n", er, eg, eb, ea, "\n", ar, ag, ab, aa, aa)
				return
			}
		}
	}
}

func TestImageHandler(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	football := getImage("test_data/football.png", t)

	proxy.HandleResponse(goproxy.UrlIsIn("/test_data/panda.png")(goproxy_image.HandleImage(func(img image.Image, ctx *goproxy.ProxyCtx) image.Image {
		return football
	})))

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	resp, err := client.Get(localFile("test_data/panda.png"))
	if err != nil {
		t.Fatal("Cannot get panda.png", err)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		t.Error("decode", err)
	} else {
		compareImage(football, img, t)
	}

	// and again
	resp, err = client.Get(localFile("test_data/panda.png"))
	if err != nil {
		t.Fatal("Cannot get panda.png", err)
	}

	img, _, err = image.Decode(resp.Body)
	if err != nil {
		t.Error("decode", err)
	} else {
		compareImage(football, img, t)
	}
}

func TestChangeResp(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleResponseFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		ctx.Resp.Body.Read([]byte{0})
		ctx.Resp.Body = ioutil.NopCloser(new(bytes.Buffer))
		return goproxy.NEXT
	})

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	resp, err := client.Get(localFile("test_data/panda.png"))
	if err != nil {
		t.Fatal(err)
	}
	ioutil.ReadAll(resp.Body)
	_, err = client.Get(localFile("/bobo"))
	if err != nil {
		t.Fatal(err)
	}
}
func TestReplaceImage(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()

	panda := getImage("test_data/panda.png", t)
	football := getImage("test_data/football.png", t)

	proxy.HandleResponse(goproxy.UrlIsIn("/test_data/panda.png")(goproxy_image.HandleImage(func(img image.Image, ctx *goproxy.ProxyCtx) image.Image {
		return football
	})))

	proxy.HandleResponse(goproxy.UrlIsIn("/test_data/football.png")(goproxy_image.HandleImage(func(img image.Image, ctx *goproxy.ProxyCtx) image.Image {
		return panda
	})))

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	imgByPandaReq, _, err := image.Decode(bytes.NewReader(getOrFail(localFile("test_data/panda.png"), client, t)))
	fatalOnErr(err, "decode panda", t)
	compareImage(football, imgByPandaReq, t)

	imgByFootballReq, _, err := image.Decode(bytes.NewReader(getOrFail(localFile("test_data/football.png"), client, t)))
	fatalOnErr(err, "decode football", t)
	compareImage(panda, imgByFootballReq, t)
}

func getCert(c *tls.Conn, t *testing.T) []byte {
	if err := c.Handshake(); err != nil {
		t.Fatal("cannot handshake", err)
	}
	return c.ConnectionState().PeerCertificates[0].Raw
}

func TestSimpleMitmWithSNI(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleConnectFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		ctx.SNIHost()
		return goproxy.MITM
	})

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	if resp := string(getOrFail(https.URL+"/bobo", client, t)); resp != "bobo" {
		t.Error("Wrong response when mitm", resp, "expected bobo")
	}
	if resp := string(getOrFail(https.URL+"/query?result=bar", client, t)); resp != "bar" {
		t.Error("Wrong response when mitm", resp, "expected bar")
	}
}

func TestSimpleMitmWithoutSNI(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleConnectFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		return goproxy.MITM
	})

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	if resp := string(getOrFail(https.URL+"/bobo", client, t)); resp != "bobo" {
		t.Error("Wrong response when mitm", resp, "expected bobo")
	}
	if resp := string(getOrFail(https.URL+"/query?result=bar", client, t)); resp != "bar" {
		t.Error("Wrong response when mitm", resp, "expected bar")
	}
}

func TestMitmDynamicCertificate(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleConnect(goproxy.AlwaysMitm)

	_, l := oneShotProxy(proxy, t)
	defer l.Close()

	c, err := tls.Dial("tcp", https.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal("cannot dial to tcp server", err)
	}
	origCert := getCert(c, t)
	c.Close()

	c2, err := net.Dial("tcp", l.Listener.Addr().String())
	if err != nil {
		t.Fatal("dialing to proxy", err)
	}
	creq, err := http.NewRequest("CONNECT", https.URL, nil)
	//creq,err := http.NewRequest("CONNECT","https://google.com:443",nil)
	if err != nil {
		t.Fatal("create new request", creq)
	}
	creq.Write(c2)
	c2buf := bufio.NewReader(c2)
	resp, err := http.ReadResponse(c2buf, creq)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal("Cannot CONNECT through proxy", err)
	}
	c2tls := tls.Client(c2, &tls.Config{InsecureSkipVerify: true})
	proxyCert := getCert(c2tls, t)

	if bytes.Equal(proxyCert, origCert) {
		t.Errorf("Certificate after mitm is not different\n%v\n%v",
			base64.StdEncoding.EncodeToString(origCert),
			base64.StdEncoding.EncodeToString(proxyCert))
	}
}

func TestConnectHandler(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	althttps := httptest.NewTLSServer(ConstantHandler("althttps"))
	proxy.HandleConnectFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		u, _ := url.Parse(althttps.URL)
		ctx.SetDestinationHost(u.Host)
		return goproxy.FORWARD
	})

	client, l := oneShotProxy(proxy, t)
	defer l.Close()
	if resp := string(getOrFail(https.URL+"/alturl", client, t)); resp != "althttps" {
		t.Error("Proxy should redirect CONNECT requests to local althttps server, expected 'althttps' got ", resp)
	}
}

func TestMitmIsFiltered(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	//proxy.Verbose = true
	proxy.HandleConnect(goproxy.AlwaysMitm)
	// PREVIOUSLY: proxy.OnRequest(goproxy.ReqHostIs(https.Listener.Addr().String())).HandleConnect(goproxy.AlwaysMitm)
	proxy.HandleRequest(goproxy.UrlIsIn("/momo")(goproxy.HandlerFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		ctx.NewTextResponse("koko")
		return goproxy.FORWARD
	})))

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	if resp := string(getOrFail(https.URL+"/momo", client, t)); resp != "koko" {
		t.Error("Proxy should capture /momo to be koko and not", resp)
	}

	if resp := string(getOrFail(https.URL+"/bobo", client, t)); resp != "bobo" {
		t.Error("But still /bobo should be 'bobo' and not", resp)
	}
}

func TestFirstHandlerMatches(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleRequestFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		ctx.NewTextResponse("koko")
		return goproxy.FORWARD
	})
	proxy.HandleRequestFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		panic("should never get here, because of the previous FORWARD")
	})

	client, l := oneShotProxy(proxy, t)
	defer l.Close()

	if resp := string(getOrFail(srv.URL+"/", client, t)); resp != "koko" {
		t.Error("should return always koko and not", resp)
	}
}

func constantHttpServer(content []byte) (addr string) {
	l, err := net.Listen("tcp", "localhost:0")
	panicOnErr(err, "listen")
	go func() {
		c, err := l.Accept()
		panicOnErr(err, "accept")
		buf := bufio.NewReader(c)
		_, err = http.ReadRequest(buf)
		panicOnErr(err, "readReq")
		c.Write(content)
		c.Close()
		l.Close()
	}()
	return l.Addr().String()
}

func TestIcyResponse(t *testing.T) {
	// TODO: fix this test
	return // skip for now
	s := constantHttpServer([]byte("ICY 200 OK\r\n\r\nblablabla"))
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = true
	_, l := oneShotProxy(proxy, t)
	defer l.Close()
	req, err := http.NewRequest("GET", "http://"+s, nil)
	panicOnErr(err, "newReq")
	proxyip := l.URL[len("http://"):]
	println("got ip: " + proxyip)
	c, err := net.Dial("tcp", proxyip)
	panicOnErr(err, "dial")
	defer c.Close()
	req.WriteProxy(c)
	raw, err := ioutil.ReadAll(c)
	panicOnErr(err, "readAll")
	if string(raw) != "ICY 200 OK\r\n\r\nblablabla" {
		t.Error("Proxy did not send the malformed response received")
	}
}

type VerifyNoProxyHeaders struct {
	*testing.T
}

func (v VerifyNoProxyHeaders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Connection") != "" || r.Header.Get("Proxy-Connection") != "" {
		v.Error("Got Connection header from goproxy", r.Header)
	}
}

func TestNoProxyHeaders(t *testing.T) {
	s := httptest.NewServer(VerifyNoProxyHeaders{t})
	client, l := oneShotProxy(goproxy.NewProxyHttpServer(), t)
	defer l.Close()
	req, err := http.NewRequest("GET", s.URL, nil)
	panicOnErr(err, "bad request")
	req.Header.Add("Connection", "close")
	req.Header.Add("Proxy-Connection", "close")
	client.Do(req)
}

func TestNoProxyHeadersHttps(t *testing.T) {
	s := httptest.NewTLSServer(VerifyNoProxyHeaders{t})
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleConnect(goproxy.AlwaysMitm)
	client, l := oneShotProxy(proxy, t)
	defer l.Close()
	req, err := http.NewRequest("GET", s.URL, nil)
	panicOnErr(err, "bad request")
	req.Header.Add("Connection", "close")
	req.Header.Add("Proxy-Connection", "close")
	client.Do(req)
}

func TestHeadReqHasContentLength(t *testing.T) {
	client, l := oneShotProxy(goproxy.NewProxyHttpServer(), t)
	defer l.Close()

	resp, err := client.Head(localFile("test_data/panda.png"))
	panicOnErr(err, "resp to HEAD")
	if resp.Header.Get("Content-Length") == "" {
		t.Error("Content-Length should exist on HEAD requests")
	}
}

func TestChunkedResponse(t *testing.T) {
	l, err := net.Listen("tcp", ":10234")
	panicOnErr(err, "listen")
	defer l.Close()
	go func() {
		for i := 0; i < 2; i++ {
			c, err := l.Accept()
			panicOnErr(err, "accept")
			_, err = http.ReadRequest(bufio.NewReader(c))
			panicOnErr(err, "readrequest")
			io.WriteString(c, "HTTP/1.1 200 OK\r\n"+
				"Content-Type: text/plain\r\n"+
				"Transfer-Encoding: chunked\r\n\r\n"+
				"25\r\n"+
				"This is the data in the first chunk\r\n\r\n"+
				"1C\r\n"+
				"and this is the second one\r\n\r\n"+
				"3\r\n"+
				"con\r\n"+
				"8\r\n"+
				"sequence\r\n0\r\n\r\n")
			c.Close()
		}
	}()

	c, err := net.Dial("tcp", "localhost:10234")
	panicOnErr(err, "dial")
	defer c.Close()
	req, _ := http.NewRequest("GET", "/", nil)
	req.Write(c)
	resp, err := http.ReadResponse(bufio.NewReader(c), req)
	panicOnErr(err, "readresp")
	b, err := ioutil.ReadAll(resp.Body)
	panicOnErr(err, "readall")
	expected := "This is the data in the first chunk\r\nand this is the second one\r\nconsequence"
	if string(b) != expected {
		t.Errorf("Got `%v` expected `%v`", string(b), expected)
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleResponseFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		resp := ctx.Resp
		panicOnErr(ctx.Error, "error reading output")
		b, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		panicOnErr(err, "readall onresp")
		if enc := resp.Header.Get("Transfer-Encoding"); enc != "" {
			t.Fatal("Chunked response should be received as plaintext", enc)
		}
		resp.Body = ioutil.NopCloser(bytes.NewBufferString(strings.Replace(string(b), "e", "E", -1)))
		return goproxy.NEXT
	})

	client, s := oneShotProxy(proxy, t)
	defer s.Close()

	resp, err = client.Get("http://localhost:10234/")
	panicOnErr(err, "client.Get")
	b, err = ioutil.ReadAll(resp.Body)
	panicOnErr(err, "readall proxy")
	if string(b) != strings.Replace(expected, "e", "E", -1) {
		t.Error("expected", expected, "w/ e->E. Got", string(b))
	}
}

func TestGoproxyThroughProxy(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy2 := goproxy.NewProxyHttpServer()
	doubleString := goproxy.HandlerFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		resp := ctx.Resp
		b, err := ioutil.ReadAll(resp.Body)
		panicOnErr(err, "readAll resp")
		resp.Body = ioutil.NopCloser(bytes.NewBufferString(string(b) + " " + string(b)))
		return goproxy.NEXT
	})
	proxy.HandleConnect(goproxy.AlwaysMitm)
	proxy.HandleResponse(doubleString)

	_, l := oneShotProxy(proxy, t)
	defer l.Close()

	proxy2.ConnectDial = proxy2.NewConnectDialToProxy(l.URL)

	client, l2 := oneShotProxy(proxy2, t)
	defer l2.Close()
	if r := string(getOrFail(https.URL+"/bobo", client, t)); r != "bobo bobo" {
		t.Error("Expected bobo doubled twice, got", r)
	}

}

func TestGoproxyHijackConnect(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()

	hijackHandler := goproxy.HandlerFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		req := ctx.Req

		client := ctx.HijackConnect()

		t.Logf("URL %+#v\nSTR %s", req.URL, req.URL.String())
		resp, err := http.Get("http:" + req.URL.String() + "/bobo")
		panicOnErr(err, "http.Get(CONNECT url)")
		panicOnErr(resp.Write(client), "resp.Write(client)")
		resp.Body.Close()
		client.Close()

		return goproxy.DONE
	})
	proxy.HandleRequest(goproxy.RequestHostIsIn(srv.Listener.Addr().String())(hijackHandler))

	client, l := oneShotProxy(proxy, t)
	defer l.Close()
	proxyAddr := l.Listener.Addr().String()
	conn, err := net.Dial("tcp", proxyAddr)
	panicOnErr(err, "conn "+proxyAddr)
	buf := bufio.NewReader(conn)
	writeConnect(conn)
	readConnectResponse(buf)
	if txt := readResponse(buf); txt != "bobo" {
		t.Error("Expected bobo for CONNECT /foo, got", txt)
	}

	if r := string(getOrFail(https.URL+"/bobo", client, t)); r != "bobo" {
		t.Error("Expected bobo would keep working with CONNECT", r)
	}
}

func readResponse(buf *bufio.Reader) string {
	req, err := http.NewRequest("GET", srv.URL, nil)
	panicOnErr(err, "NewRequest")
	resp, err := http.ReadResponse(buf, req)
	panicOnErr(err, "resp.Read")
	defer resp.Body.Close()
	txt, err := ioutil.ReadAll(resp.Body)
	panicOnErr(err, "resp.Read")
	return string(txt)
}

func writeConnect(w io.Writer) {
	req, err := http.NewRequest("CONNECT", srv.URL[len("http://"):], nil)
	panicOnErr(err, "NewRequest")
	req.Write(w)
	panicOnErr(err, "req(CONNECT).Write")
}

func readConnectResponse(buf *bufio.Reader) {
	_, err := buf.ReadString('\n')
	panicOnErr(err, "resp.Read connect resp")
	_, err = buf.ReadString('\n')
	panicOnErr(err, "resp.Read connect resp")
}

func TestCurlMinusP(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleConnectFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		return goproxy.MITM // default host
	})
	called := false
	proxy.HandleRequestFunc(func(ctx *goproxy.ProxyCtx) goproxy.Next {
		called = true
		return goproxy.NEXT
	})

	_, l := oneShotProxy(proxy, t)
	defer l.Close()

	cmd := exec.Command("curl", "-p", "-sS", "--proxy", l.URL, srv.URL+"/bobo")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "bobo" {
		t.Error("Expected bobo, got", string(output))
	}
	if !called {
		t.Error("handler not called")
	}
}

func TestSelfRequest(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	_, l := oneShotProxy(proxy, t)
	defer l.Close()
	if !strings.Contains(string(getOrFail(l.URL, http.DefaultClient, t)), "non-proxy") {
		t.Fatal("non proxy requests should fail")
	}
}

func TestHasGoproxyCA(t *testing.T) {
	proxy := goproxy.NewProxyHttpServer()
	proxy.HandleConnect(goproxy.AlwaysMitm)
	s := httptest.NewServer(proxy)

	proxyUrl, _ := url.Parse(s.URL)
	goproxyCA := x509.NewCertPool()
	goproxyCA.AddCert(goproxy.GoproxyCa.Leaf)

	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: goproxyCA}, Proxy: http.ProxyURL(proxyUrl)}
	client := &http.Client{Transport: tr}

	if resp := string(getOrFail(https.URL+"/bobo", client, t)); resp != "bobo" {
		t.Error("Wrong response when mitm", resp, "expected bobo")
	}
}
