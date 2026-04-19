docker run -it --rm -p 3306:3306 -e MYSQL_ROOT_PASSWORD=H2aF8X4jQ9sqA. -e MYSQL_DATABASE=meta_node_storage mysql


# docker ps
# docker exec -it 55fb71803043 mysql -u root -p




# docker exec -it $(docker ps -q --filter "ancestor=mysql") mysql -u root -pH2aF8X4jQ9sqA. -D meta_node_storage


# thuc thi cau lenh kiem tra so dong
# docker exec -it $(docker ps -q --filter "ancestor=mysql") mysql -u root -pH2aF8X4jQ9sqA. -D meta_node_storage -e "select count(*) from logs"


