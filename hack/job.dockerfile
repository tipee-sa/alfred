FROM alpine:latest

ADD job.sh /job.sh
RUN chmod +x /job.sh

CMD ["/job.sh"]
