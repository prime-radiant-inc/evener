package appwire

import "context"

type Transport interface {
	Send(context.Context, Message) error
	Recv(context.Context) (Message, error)
	Close() error
}
