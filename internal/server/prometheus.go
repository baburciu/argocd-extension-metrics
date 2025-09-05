package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"go.uber.org/zap"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
)

// ThresholdResponse represents the response format for a threshold.
type ThresholdResponse struct {
	Data  json.RawMessage `json:"data"`
	Key   string          `json:"key"`
	Name  string          `json:"name"`
	Color string          `json:"color"`
	Value string          `json:"value"`
	Unit  string          `json:"unit"`
}

// AggregatedResponse represents the final output response structure returned by execute function
type AggregatedResponse struct {
	Data       json.RawMessage     `json:"data"`
	Thresholds []ThresholdResponse `json:"thresholds,omitempty"`
}

type PrometheusProvider struct {
	logger   *zap.SugaredLogger
	provider v1.API
	config   *MetricsConfigProvider
	skipTLSVerify bool
}

// Custom RoundTripper to add headers
type headerRoundTripper struct {
	headers map[string]string
	rt      http.RoundTripper
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Log the URL being requested (without the API key for security)
	fmt.Printf("Making request to: %s\n", req.URL.String())

	// Add all headers
	for k, v := range h.headers {
		req.Header.Add(k, v)
		// Log header names (but not values for security)
		if k != "apikey" {
			fmt.Printf("Added header: %s: %s\n", k, v)
		} else {
			fmt.Printf("Added header: %s: [REDACTED]\n", k)
		}
	}

	// Show all request headers for debugging
	fmt.Println("All request headers:")
	for k, v := range req.Header {
		if k != "apikey" {
			fmt.Printf("  %s: %s\n", k, v)
		} else {
			fmt.Printf("  %s: [REDACTED]\n", k)
		}
	}

	return h.rt.RoundTrip(req)
}

func (pp *PrometheusProvider) getType() string {
	return PROMETHEUS_TYPE
}

// getDashboard returns the dashboard configuration for the specified application
func (pp *PrometheusProvider) getDashboard(ctx *gin.Context) {
	appName := ctx.Param("application")
	groupKind := ctx.Param("groupkind")
	app := pp.config.getApp(appName)
	if app == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Application not found")
		return
	}
	dash := app.getDashBoard(groupKind)

	if dash == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Dashboard not found")
		return
	}
	dash.ProviderType = pp.getType()
	ctx.JSON(http.StatusOK, dash)
}

func NewPrometheusProvider(prometheusConfig *MetricsConfigProvider, logger *zap.SugaredLogger, skipTLSVerify bool) *PrometheusProvider {
	return &PrometheusProvider{
		config: prometheusConfig,
		logger: logger,
		skipTLSVerify: skipTLSVerify,
	}
}

func (pp *PrometheusProvider) init() error {
	// Create config with headers support
	clientConfig := api.Config{
		Address: pp.config.Provider.Address,
	}

	// Set up the transport
	var transport *http.Transport

	// Apply TLS skip verification if requested
	if pp.skipTLSVerify {
		pp.logger.Info("Skipping TLS certificate verification for Prometheus connections")
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Skip certificate verification
			},
		}
	} else {
		// Use default transport with normal TLS verification
		transport = &http.Transport{}
	}

	// Check for environment variable PROMETHEUS_APIKEY
	if apiKey := os.Getenv("PROMETHEUS_APIKEY"); apiKey != "" {
		pp.logger.Info("Using PROMETHEUS_APIKEY from environment variable")
		clientConfig.RoundTripper = &headerRoundTripper{
			headers: map[string]string{"apikey": apiKey},
			rt:      transport,
		}
	} else {
		// No headers, but still need to use our transport
		clientConfig.RoundTripper = transport
	}

	client, err := api.NewClient(clientConfig)
	if err != nil {
		pp.logger.Errorf("Error creating client: %v\n", err)
		return err
	}
	pp.provider = v1.NewAPI(client)
	return nil
}

// executeGraphQuery executes a prometheus query and returns the result.
func executeGraphQuery(ctx *gin.Context, queryExpression string, env map[string][]string, duration time.Duration, pp *PrometheusProvider) (model.Value, v1.Warnings, error) {
	tmpl, err := template.New("query").Parse(queryExpression)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing query template: %s", err)
	}

	env1 := make(map[string]string)
	for k, v := range env {
		env1[k] = strings.Join(v, ",")
	}

	buf := new(bytes.Buffer)
	err = tmpl.Execute(buf, env1)
	if err != nil {
		return nil, nil, fmt.Errorf("error executing template: %s", err)
	}

	strQuery := buf.String()
	r := v1.Range{
		Start: time.Now().Add(-duration),
		End:   time.Now(),
		Step:  time.Minute,
	}

	fmt.Printf("Executing Prometheus query: %s\n", strQuery)
	fmt.Printf("Time range: start=%v, end=%v, step=%v\n", r.Start, r.End, r.Step)

	result, warnings, err := pp.provider.QueryRange(ctx, strQuery, r)

	if err != nil {
		pp.logger.Errorf("Error querying prometheus at %s: %s, query: %s", pp.config.Provider.Address, err, strQuery)
		pp.logger.Errorf("Provider config: Address: %s, Name: %s", pp.config.Provider.Address, pp.config.Provider.Name)
		return nil, warnings, fmt.Errorf("error querying prometheus: %s", err)
	}

	// Log the result type and some details
	fmt.Printf("Query result type: %T\n", result)
	if result != nil {
		switch v := result.(type) {
		case model.Matrix:
			fmt.Printf("Matrix result with %d series\n", len(v))
			if len(v) > 0 {
				fmt.Printf("First series has %d samples\n", len(v[0].Values))
				if len(v[0].Values) > 0 {
					fmt.Printf("Sample values: %v\n", v[0].Values[0].Value)
				} else {
					fmt.Printf("No samples in first series\n")
				}
			} else {
				fmt.Printf("No series in matrix\n")
			}
		case model.Vector:
			fmt.Printf("Vector result with %d samples\n", len(v))
		case *model.Scalar:
			fmt.Printf("Scalar pointer result: %v\n", v.Value)
		case *model.String:
			fmt.Printf("String pointer result: %s\n", v.Value)
		default:
			fmt.Printf("Unknown result type\n")
		}
	} else {
		fmt.Printf("Query result is nil\n")
	}

	if len(warnings) > 0 {
		pp.logger.Warnf("Query warnings: %v", warnings)
		return result, warnings, fmt.Errorf("query warnings: %s", warnings)
	}

	return result, nil, nil
}

// execute handles the execution of a graph queryExpression and graph thresholds
func (pp *PrometheusProvider) execute(ctx *gin.Context) {
	app := ctx.Param("application")
	groupKind := ctx.Param("groupkind")
	rowName := ctx.Param("row")
	graphName := ctx.Param("graph")
	durationStr := ctx.Query("duration")
	if durationStr == "" {
		durationStr = "1h"
	}
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, "Invalid duration format :"+err.Error())
		return
	}

	env := ctx.Request.URL.Query()

	application := pp.config.getApp(app)
	if application == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Application not found")
		return
	}
	dashboard := application.getDashBoard(groupKind)
	if dashboard == nil {
		ctx.JSON(http.StatusBadRequest, "Requested/Default Dashboard not found")
		return
	}
	row := dashboard.getRow(rowName)
	if row == nil {
		ctx.JSON(http.StatusBadRequest, "Requested Row not found")
		return
	}
	graph := row.getGraph(graphName)
	if graph != nil {

		var data AggregatedResponse
		result, warnings, err := executeGraphQuery(ctx, graph.QueryExpression, env, duration, pp)

		if err != nil {
			pp.logger.Errorf("Error executing graph query: %v", err)
			ctx.JSON(http.StatusBadRequest, err)
			return
		}
		if len(warnings) > 0 {
			warningMsg := fmt.Errorf("query warnings: %s", warnings)
			pp.logger.Warnf("Query warnings: %v", warnings)
			ctx.JSON(http.StatusBadRequest, warningMsg.Error())
			return
		}
		data.Data, err = json.Marshal(result)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, fmt.Errorf("error marshaling the data: %s", err))
			return
		}

		// Log the data being returned
		jsonString, _ := json.MarshalIndent(data, "", "  ")
		fmt.Printf("Returning data to UI: %s\n", string(jsonString))
		var finalResultArr []ThresholdResponse
		if graph.Thresholds != nil {

			for _, threshold := range graph.Thresholds {
				var result model.Value
				var warnings v1.Warnings
				var err error

				//If threshold.value present, threshold.value gets executed else,threshold.queryExpression gets executed.
				if threshold.Value != "" {
					result, warnings, err = executeGraphQuery(ctx, threshold.Value, env, duration, pp)
				} else {
					result, warnings, err = executeGraphQuery(ctx, threshold.QueryExpression, env, duration, pp)
				}
				if err != nil {
					ctx.JSON(http.StatusBadRequest, err)
					return
				}
				if len(warnings) > 0 {
					warningMsg := fmt.Errorf("query warnings: %s", warnings)
					ctx.JSON(http.StatusBadRequest, warningMsg.Error())
					return
				}
				var temp ThresholdResponse
				temp.Unit = threshold.Unit
				temp.Name = threshold.Name
				temp.Value = threshold.Value
				temp.Key = threshold.Key
				temp.Color = threshold.Color
				temp.Data, err = json.Marshal(result)
				if err != nil {
					ctx.JSON(http.StatusBadRequest, fmt.Errorf("error marshaling the threshold response: %s", err))
					return
				}

				finalResultArr = append(finalResultArr, temp)
			}
		}
		data.Thresholds = finalResultArr

		ctx.JSON(http.StatusOK, data)
		return
	}
}
