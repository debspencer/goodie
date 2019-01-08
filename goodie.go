package goodie

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/debspencer/html"
)

var (
	defaultReadTimeout    = 10 * time.Second
	defaultWriteTimeout   = 10 * time.Second
	defaultMaxHeaderBytes = 16 * 1024

	NotFound    = errors.New("Not Found")
	ServerError = errors.New("Internal Server Error")
)

type NewHandler func() Handler

type Server struct {
	Addr           string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxHeaderBytes int

	handlers map[string]NewHandler
}

type App struct {
	odie *Server
	name string
}

type Handler interface {
	render(w http.ResponseWriter, req *http.Request, handler Handler) // implemented by Odie

	// Init App.  Returns slice of urls showing stack (index -> page1 -> page2)
	// Optional, if []byte is returned, then that data is written
	Init() ([]*html.URL, []byte, error)
	Action(string, *html.URL) bool
	Header([]*html.URL)
	Display()
	Footer([]*html.URL)
}

func Init(addr string, o *Server) *Server {
	if o == nil {
		o = &Server{
			ReadTimeout:    defaultReadTimeout,
			WriteTimeout:   defaultWriteTimeout,
			MaxHeaderBytes: defaultMaxHeaderBytes,
		}
	}
	o.Addr = addr
	o.handlers = make(map[string]NewHandler)
	return o
}

func (o *Server) NewApp(name string) *App {
	return &App{
		odie: o,
		name: name,
	}
}

func (a *App) Register(page string, h NewHandler) {
	if len(page) > 0 && !strings.HasPrefix(page, "/") {
		page = "/" + page
	}
	page = "/" + a.name + page
	fmt.Println("Register:", page)
	a.odie.handlers[page] = h
}

func (o *Server) Run() error {
	s := &http.Server{
		Addr:           o.Addr,
		Handler:        o,
		ReadTimeout:    o.ReadTimeout,
		WriteTimeout:   o.WriteTimeout,
		MaxHeaderBytes: o.MaxHeaderBytes,
	}
	return s.ListenAndServe()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path
	fmt.Println("Request:", path)
	handlerMaker, ok := s.handlers[path]
	if !ok {
		fmt.Printf("404 = '%s'\n", req.URL.Path)
		w.WriteHeader(404)
		return
	}

	handler := handlerMaker()
	handler.render(w, req, handler)
}

type Odie struct {
	Request  *http.Request
	Response http.ResponseWriter
	Doc      *html.Document
	Body     *html.BodyElement
}

// Render will create an HTML docuement and render the page
func (odie *Odie) render(w http.ResponseWriter, req *http.Request, handler Handler) {
	odie.Request = req
	odie.Response = w

	// create the HTML doc, but don't add a body to it yet
	odie.Doc = html.NewDocument()
	odie.Doc.AddCSS(html.CSS(default_css))

	// call handler's init method.  It will return the base named.
	urls, data, err := handler.Init()

	var topurl *html.URL
	if len(urls) > 0 {
		topurl = urls[len(urls)-1]
	}

	refreshUrl := topurl
	if refreshUrl == nil {
		refreshUrl = odie.DefaultURL()
	}

	action := refreshUrl.GetQuery("action")
	if len(action) > 0 {
		refresh := handler.Action(action, refreshUrl)
		if refresh {
			refreshUrl.DelQuery("action") // remove action so we don't go into an infinite loop

			odie.Doc.Head().Add(html.MetaRefresh(0, refreshUrl.Link()))
			b := &bytes.Buffer{}
			odie.Doc.Render(b)
			odie.Response.Write(b.Bytes())
			return
		}
	}

	// create html body now we are ready to render
	odie.Body = odie.Doc.Body()
	odie.Body.AddClassName("goodiebody")

	if err != nil {
		if err == NotFound {
			w.WriteHeader(404)
			return
		}
		if err == ServerError {
			w.WriteHeader(500)
			return
		}
	}

	if data != nil {
		odie.Response.Write(data)
		return
	}

	// Set a default title if init did not do so
	title := odie.Doc.Head().GetTitle()
	if len(title) == 0 && topurl != nil {
		odie.Doc.Head().AddTitle(topurl.Name)
	}

	handler.Header(urls)
	handler.Display()
	handler.Footer(urls)

	b := &bytes.Buffer{}
	odie.Doc.Render(b)
	odie.Response.Write(b.Bytes())
}

func (odie *Odie) DefaultURL() *html.URL {
	u := html.NewURL(odie.Request.URL)
	u.Name = odie.Request.URL.Path
	return u
}
func (odie *Odie) HomeURL() *html.URL {
	u := html.NewURL(odie.Request.URL)
	u.Name = "Home"
	u.App = ""
	u.Page = "/"
	fmt.Printf("%#v\n", u)
	return u
}

func (odie *Odie) NewForm(action string) *html.FormElement {
	l := html.NewLink(odie.Request.URL.Path)
	f := html.Form(l)
	qs := odie.Request.URL.Query()
	for k, vs := range qs {
		var v string
		if len(vs) > 0 {
			v = vs[0]
		}
		f.Add(html.Hidden(k, v))
	}
	if len(action) > 0 {
		f.Add(html.Hidden("action", action))
	}
	return f
}

func (odie *Odie) showHeader(which string, urls []*html.URL) {
	div := html.Div()
	div.AddClassName("goodie" + which)
	h := html.Heading(3, urlStack(urls))
	div.Add(h)
	odie.Body.Add(div)
}

func urlStack(urls []*html.URL) html.Element {
	div := html.Div()
	for i, url := range urls {
		// last one
		if i == len(urls)-1 {
			div.Add(html.Text(url.Name))
		} else {
			div.Add(url)
			div.Add(html.Text(">"))
		}
	}
	return div
}

// Override methods

// Init before page is displayed.   No access to HTML at this time
func (odie *Odie) Init() []*html.URL {
	return []*html.URL{odie.DefaultURL()}
}

// Perform an action before the page loads.
// return value of true = refresh page without action query string.  The allows for db updates and a page reload would not do a double action
// return value of falese will continue on to render.  A page reload will recall action
// Controlled by presense of action= query string
// url is passed so action can have easy access to query paramters
func (odie *Odie) Action(action string, url *html.URL) bool {
	return false
}

// Header will show the page header
func (odie *Odie) Header(urls []*html.URL) {
	odie.showHeader("header", urls)
}

// Display will show render page between header and footer
func (odie *Odie) Display() {
}

// Footer will show the page footer
func (odie *Odie) Footer(urls []*html.URL) {
	odie.showHeader("footer", urls)
}
