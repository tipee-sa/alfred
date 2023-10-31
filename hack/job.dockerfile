FROM mysql:8.0.31

ADD job.sh /job.sh
RUN chmod +x /job.sh

CMD ["/job.sh"]
