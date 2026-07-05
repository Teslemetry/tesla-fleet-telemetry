package config

const TestConfig = `{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"log_level": "info",
	"json_log_enable": true,
	"namespace": "tesla_telemetry",
	"reliable_ack_sources": {
		"V": "nats"
	},
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"monitoring": {
		"prometheus_metrics_port": 9090,
		"profiler_port": 4269,
		"profiling_path": "/tmp/fleet-telemetry/profile"
	},
	"rate_limit": {
		"enabled": true,
		"message_interval_time": 30,
		"message_limit": 1000
	},
	"records": {
		"V": ["nats"]
	},
	"tls": {
		"ca_file": "tesla.ca",
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`

const TestSmallConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats"]
	},
	"tls": {
		"ca_file": "tesla.ca",
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`

const BadVinsConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats"]
	},
	"vins_signal_tracking_enabled": ["vin1", "vin2", "vin3"],
	"tls": {
		"ca_file": "tesla.ca",
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`
const TestBadReliableAckConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"reliable_ack_sources": {
		"V": "some_unconfigured_dispatcher"
	},
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats"]
	},
	"tls": {
		"ca_file": "tesla.ca",
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`

const TestLoggerAsReliableAckConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"reliable_ack_sources": {
		"V": "logger"
	},
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats", "logger"]
	},
	"tls": {
		"ca_file": "tesla.ca",
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`

const TestUnusedTxTypeAsReliableAckConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"reliable_ack_sources": {
		"error": "nats"
	},
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats", "logger"]
	},
	"tls": {
		"ca_file": "tesla.ca",
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`

const BadTopicConfig = `
{
	"host": "127.0.0.1",
	"port": "",
}`

const TestTransmitDecodedRecords = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"transmit_decoded_records": true,
	"records": {
		"V": ["logger"]
	}
}
`

const TestVinsToTrackConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"transmit_decoded_records": true,
	"records": {
		"V": ["logger"]
	},
	"vins_signal_tracking_enabled": ["v1", "v2"]
}
`

const TestAirbrakeConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats"]
	},
	"tls": {
		"ca_file": "tesla.ca",
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	},
	"airbrake": {
        "project_id": 1,
        "project_key": "test1",
        "environment": "integration",
        "host": "http://errbit-test.example.com"
    }
}
`

const TestBadTxTypeReliableAckConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"reliable_ack_sources": {
		"connectivity": "nats"
	},
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats"],
		"connectivity": ["nats"]
	},
	"tls": {
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`

const TestMultipleTxTypeReliableAckConfig = `
{
	"host": "127.0.0.1",
	"port": 443,
	"status_port": 8080,
	"namespace": "tesla_telemetry",
	"reliable_ack_sources": {
		"V": "nats",
		"errors": "nats",
		"alerts": "nats"
	},
	"nats": {
		"url": "nats://some.broker1:4222",
		"name": "fleet-telemetry"
	},
	"records": {
		"V": ["nats"],
		"errors": ["nats"],
		"alerts": ["nats"]
	},
	"tls": {
		"server_cert": "your_own_cert.crt",
		"server_key": "your_own_key.key"
	}
}
`
