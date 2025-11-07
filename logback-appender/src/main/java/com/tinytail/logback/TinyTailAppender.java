package com.tinytail.logback;

import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.AppenderBase;

import java.io.IOException;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ThreadFactory;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

public class TinyTailAppender extends AppenderBase<ILoggingEvent> {

    private String endpoint;
    private String source = "unknown";
    private String secret;
    private int connectTimeout = 5000;
    private int readTimeout = 5000;
    private int maxRetries = 1;
    private int retryDelayMs = 200;
    private ExecutorService executor;

    @Override
    public void start() {
        if (endpoint == null || endpoint.trim().isEmpty()) {
            addError("Endpoint is required for TinyTailAppender");
            return;
        }

        if (secret == null || secret.trim().isEmpty()) {
            addError("Secret is required for TinyTailAppender");
            return;
        }

        ThreadFactory threadFactory = new ThreadFactory() {
            private final AtomicInteger counter = new AtomicInteger(0);

            @Override
            public Thread newThread(Runnable r) {
                Thread thread = new Thread(r, "TinyTail-" + counter.incrementAndGet());
                thread.setDaemon(true);
                return thread;
            }
        };

        executor = Executors.newFixedThreadPool(2, threadFactory);
        super.start();
    }

    @Override
    public void stop() {
        super.stop();
        if (executor != null) {
            executor.shutdown();
            try {
                if (!executor.awaitTermination(10, TimeUnit.SECONDS)) {
                    executor.shutdownNow();
                }
            }
            catch (InterruptedException e) {
                executor.shutdownNow();
                Thread.currentThread().interrupt();
            }
        }
    }

    @Override
    protected void append(ILoggingEvent event) {
        if (executor == null || executor.isShutdown()) {
            return;
        }

        executor.submit(() -> {
            try {
                sendLogWithRetry(event);
            }
            catch (Exception e) {
                addError("Failed to send log to TinyTail after " + (maxRetries + 1) + " attempts: " + e.getMessage());
            }
        });
    }

    private void sendLogWithRetry(ILoggingEvent event) throws IOException {
        String json = buildJson(event);
        IOException lastException = null;

        for (int attempt = 0; attempt <= maxRetries; attempt++) {
            try {
                sendLog(json);
                return; // Success!
            }
            catch (IOException e) {
                lastException = e;

                // Don't retry on the last attempt
                if (attempt < maxRetries) {
                    // Check if this is a retryable error
                    if (isRetryable(e)) {
                        try {
                            Thread.sleep(retryDelayMs);
                        }
                        catch (InterruptedException ie) {
                            Thread.currentThread().interrupt();
                            throw new IOException("Retry interrupted", ie);
                        }
                    } else {
                        // Non-retryable error (auth, client error), fail immediately
                        throw e;
                    }
                }
            }
        }

        // All retries exhausted
        throw lastException;
    }

    private void sendLog(String json) throws IOException {
        HttpURLConnection conn = null;
        try {
            URI uri = URI.create(endpoint);
            conn = (HttpURLConnection) uri.toURL().openConnection();
            conn.setRequestMethod("POST");
            conn.setRequestProperty("Content-Type", "application/json");
            conn.setRequestProperty("Authorization", "Bearer " + secret);
            conn.setConnectTimeout(connectTimeout);
            conn.setReadTimeout(readTimeout);
            conn.setDoOutput(true);

            try (OutputStream os = conn.getOutputStream()) {
                os.write(json.getBytes(StandardCharsets.UTF_8));
                os.flush();
            }

            int responseCode = conn.getResponseCode();
            if (responseCode < 200 || responseCode >= 300) {
                // Throw exception for non-2xx so retry logic can handle it
                throw new IOException("TinyTail returned status " + responseCode);
            }
        }
        finally {
            if (conn != null) {
                conn.disconnect();
            }
        }
    }

    private boolean isRetryable(IOException e) {
        String message = e.getMessage();
        if (message == null) {
            return true; // Network errors without messages are retryable
        }

        // Don't retry auth failures or client errors
        if (message.contains("status 401") || message.contains("status 403") ||
            message.contains("status 400") || message.contains("status 404")) {
            return false;
        }

        // Retry on 5xx errors or network issues
        return true;
    }

    private String buildJson(ILoggingEvent event) {
        StringBuilder json = new StringBuilder();
        json.append("{");

        appendField(json, "level", event.getLevel().toString(), true);
        appendField(json, "message", formatMessage(event), false);
        appendField(json, "source", source, false);
        appendField(json, "logger", event.getLoggerName(), false);
        appendField(json, "timestamp", Instant.ofEpochMilli(event.getTimeStamp()).toString(), false);

        String requestId = event.getMDCPropertyMap().get("request_id");
        if (requestId == null) {
            requestId = event.getMDCPropertyMap().get("requestId");
        }
        if (requestId != null) {
            appendField(json, "request_id", requestId, false);
        }

        json.append("}");
        return json.toString();
    }

    private String formatMessage(ILoggingEvent event) {
        StringBuilder message = new StringBuilder();
        message.append(event.getFormattedMessage());

        if (event.getThrowableProxy() != null) {
            message.append("\n");
            appendStackTrace(message, event.getThrowableProxy());
        }

        return message.toString();
    }

    private void appendStackTrace(StringBuilder sb, ch.qos.logback.classic.spi.IThrowableProxy tp) {
        sb.append(tp.getClassName()).append(": ").append(tp.getMessage()).append("\n");

        ch.qos.logback.classic.spi.StackTraceElementProxy[] stepArray = tp.getStackTraceElementProxyArray();
        if (stepArray != null) {
            for (ch.qos.logback.classic.spi.StackTraceElementProxy step : stepArray) {
                sb.append("\tat ").append(step.toString()).append("\n");
            }
        }

        if (tp.getCause() != null) {
            sb.append("Caused by: ");
            appendStackTrace(sb, tp.getCause());
        }
    }

    private void appendField(StringBuilder json, String key, String value, boolean first) {
        if (!first) {
            json.append(",");
        }
        json.append("\"").append(escapeJson(key)).append("\":");
        json.append("\"").append(escapeJson(value)).append("\"");
    }

    private String escapeJson(String value) {
        if (value == null) {
            return "";
        }
        return value
                .replace("\\", "\\\\")
                .replace("\"", "\\\"")
                .replace("\n", "\\n")
                .replace("\r", "\\r")
                .replace("\t", "\\t")
                .replace("\b", "\\b")
                .replace("\f", "\\f");
    }

    public void setEndpoint(String endpoint) {
        this.endpoint = endpoint;
    }

    public void setSource(String source) {
        this.source = source;
    }

    public void setConnectTimeout(int connectTimeout) {
        this.connectTimeout = connectTimeout;
    }

    public void setReadTimeout(int readTimeout) {
        this.readTimeout = readTimeout;
    }

    public void setSecret(String secret) {
        this.secret = secret;
    }

    public void setMaxRetries(int maxRetries) {
        this.maxRetries = maxRetries;
    }

    public void setRetryDelayMs(int retryDelayMs) {
        this.retryDelayMs = retryDelayMs;
    }
}