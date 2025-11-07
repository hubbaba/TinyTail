## TinyTail Logback Appender

HTTP appender for sending logs from Java/JVM applications to TinyTail.

### Building

```bash
gradle build
```

This will produce `build/libs/tinytail-logback-appender-1.0.0.jar`.

### Installation

**Option 1**: Add the JAR directly to your project's classpath

**Option 2**: Publish to local Maven repository:

```bash
gradle publishToMavenLocal
```

Then add to your `build.gradle`:

```gradle
dependencies {
    implementation 'com.tinytail:tinytail-logback-appender:1.0.0'
}
```

Or for Maven `pom.xml`:

```xml
<dependency>
    <groupId>com.tinytail</groupId>
    <artifactId>tinytail-logback-appender</artifactId>
    <version>1.0.0</version>
</dependency>
```

### Configuration

Add to your `logback.xml`:

```xml
<configuration>
    <appender name="TINYTAIL" class="com.tinytail.logback.TinyTailAppender">
        <endpoint>https://your-api-gateway-url.amazonaws.com/prod/logs/ingest</endpoint>
        <source>my-application-name</source>
        <secret>your-secret-token-here</secret>
        <connectTimeout>5000</connectTimeout>
        <readTimeout>5000</readTimeout>
        <maxRetries>1</maxRetries>
        <retryDelayMs>200</retryDelayMs>
    </appender>

    <root level="INFO">
        <appender-ref ref="TINYTAIL" />
    </root>
</configuration>
```

### Configuration Properties

- **endpoint** (required): URL of your TinyTail log ingestion endpoint
- **secret** (required): Authentication token for secure log ingestion (Bearer token)
- **source** (optional): Application/service name for log identification (default: "unknown")
- **connectTimeout** (optional): Connection timeout in milliseconds (default: 5000)
- **readTimeout** (optional): Read timeout in milliseconds (default: 5000)
- **maxRetries** (optional): Number of retry attempts for failed requests (default: 1)
- **retryDelayMs** (optional): Delay between retry attempts in milliseconds (default: 200)

**Security Note**: Never commit secrets to version control! Use environment variable substitution:

```xml
<secret>${TINYTAIL_SECRET}</secret>
```

Then set the environment variable when running your application:
```bash
export TINYTAIL_SECRET="your-secret-token"
java -jar your-app.jar
```

### Request ID Tracking

The appender automatically extracts `request_id` from MDC if available.  Set it in your code:

```java
import org.slf4j.MDC;

MDC.put("request_id", "unique-request-id");
logger.info("Processing request");
MDC.remove("request_id");
```

### Features

- **Async logging**: Non-blocking fire-and-forget using thread pool
- **Smart retry logic**: Automatically retries transient failures (network issues, 5xx errors) but not auth failures
- **Large message support**: Handles Java stack traces (up to 400KB+)
- **Graceful failures**: Application continues if TinyTail is unavailable
- **Automatic stack trace formatting**: Exception details included in message