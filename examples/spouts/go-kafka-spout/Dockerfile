FROM golang:1.12.1
RUN mkdir /app 
ADD source/main.go /app/
WORKDIR /app 
RUN go get github.com/segmentio/kafka-go
RUN go build -o main . 
CMD ["/app/main"]
