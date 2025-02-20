package client

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/1Panel-dev/1Panel/backend/buserr"
	"github.com/1Panel-dev/1Panel/backend/constant"
	"github.com/1Panel-dev/1Panel/backend/global"
	"github.com/1Panel-dev/1Panel/backend/utils/files"

	"github.com/jarvanstack/mysqldump"
)

type Remote struct {
	Client   *sql.DB
	User     string
	Password string
	Address  string
	Port     uint
}

func NewRemote(db Remote) *Remote {
	return &db
}

func (r *Remote) Create(info CreateInfo) error {
	createSql := fmt.Sprintf("create database `%s` default character set %s collate %s", info.Name, info.Format, formatMap[info.Format])
	if err := r.ExecSQL(createSql, info.Timeout); err != nil {
		if strings.Contains(err.Error(), "ERROR 1007") {
			return buserr.New(constant.ErrDatabaseIsExist)
		}
		return err
	}

	if err := r.CreateUser(info); err != nil {
		return err
	}

	return nil
}

func (r *Remote) CreateUser(info CreateInfo) error {
	var userlist []string
	if strings.Contains(info.Permission, ",") {
		ips := strings.Split(info.Permission, ",")
		for _, ip := range ips {
			if len(ip) != 0 {
				userlist = append(userlist, fmt.Sprintf("'%s'@'%s'", info.Username, ip))
			}
		}
	} else {
		userlist = append(userlist, fmt.Sprintf("'%s'@'%s'", info.Username, info.Permission))
	}

	for _, user := range userlist {
		if err := r.ExecSQL(fmt.Sprintf("create user %s identified by '%s';", user, info.Password), info.Timeout); err != nil {
			_ = r.Delete(DeleteInfo{
				Name:        info.Name,
				Version:     info.Version,
				Username:    info.Username,
				Permission:  info.Permission,
				ForceDelete: true,
				Timeout:     300})
			if strings.Contains(err.Error(), "ERROR 1396") {
				return buserr.New(constant.ErrUserIsExist)
			}
			return err
		}
		grantStr := fmt.Sprintf("grant all privileges on `%s`.* to %s", info.Name, user)
		if info.Name == "*" {
			grantStr = fmt.Sprintf("grant all privileges on *.* to %s", user)
		}
		if strings.HasPrefix(info.Version, "5.7") || strings.HasPrefix(info.Version, "5.6") {
			grantStr = fmt.Sprintf("%s identified by '%s' with grant option;", grantStr, info.Password)
		}
		if err := r.ExecSQL(grantStr, info.Timeout); err != nil {
			_ = r.Delete(DeleteInfo{
				Name:        info.Name,
				Version:     info.Version,
				Username:    info.Username,
				Permission:  info.Permission,
				ForceDelete: true,
				Timeout:     300})
			return err
		}
	}
	return nil
}

func (r *Remote) Delete(info DeleteInfo) error {
	var userlist []string
	if strings.Contains(info.Permission, ",") {
		ips := strings.Split(info.Permission, ",")
		for _, ip := range ips {
			if len(ip) != 0 {
				userlist = append(userlist, fmt.Sprintf("'%s'@'%s'", info.Username, ip))
			}
		}
	} else {
		userlist = append(userlist, fmt.Sprintf("'%s'@'%s'", info.Username, info.Permission))
	}

	for _, user := range userlist {
		if strings.HasPrefix(info.Version, "5.6") {
			if err := r.ExecSQL(fmt.Sprintf("drop user %s", user), info.Timeout); err != nil && !info.ForceDelete {
				return err
			}
		} else {
			if err := r.ExecSQL(fmt.Sprintf("drop user if exists %s", user), info.Timeout); err != nil && !info.ForceDelete {
				return err
			}
		}
	}
	if len(info.Name) != 0 {
		if err := r.ExecSQL(fmt.Sprintf("drop database if exists `%s`", info.Name), info.Timeout); err != nil && !info.ForceDelete {
			return err
		}
	}
	if !info.ForceDelete {
		global.LOG.Info("execute delete database sql successful, now start to drop uploads and records")
	}

	return nil
}

func (r *Remote) ChangePassword(info PasswordChangeInfo) error {
	if info.Username != "root" {
		var userlist []string
		if strings.Contains(info.Permission, ",") {
			ips := strings.Split(info.Permission, ",")
			for _, ip := range ips {
				if len(ip) != 0 {
					userlist = append(userlist, fmt.Sprintf("'%s'@'%s'", info.Username, ip))
				}
			}
		} else {
			userlist = append(userlist, fmt.Sprintf("'%s'@'%s'", info.Username, info.Permission))
		}

		for _, user := range userlist {
			passwordChangeSql := fmt.Sprintf("set password for %s = password('%s')", user, info.Password)
			if !strings.HasPrefix(info.Version, "5.7") && !strings.HasPrefix(info.Version, "5.6") {
				passwordChangeSql = fmt.Sprintf("ALTER USER %s IDENTIFIED WITH mysql_native_password BY '%s';", user, info.Password)
			}
			if err := r.ExecSQL(passwordChangeSql, info.Timeout); err != nil {
				return err
			}
		}
		return nil
	}

	hosts, err := r.ExecSQLForHosts(info.Timeout)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if host == "%" || host == "localhost" {
			passwordRootChangeCMD := fmt.Sprintf("set password for 'root'@'%s' = password('%s')", host, info.Password)
			if !strings.HasPrefix(info.Version, "5.7") && !strings.HasPrefix(info.Version, "5.6") {
				passwordRootChangeCMD = fmt.Sprintf("alter user 'root'@'%s' identified with mysql_native_password BY '%s';", host, info.Password)
			}
			if err := r.ExecSQL(passwordRootChangeCMD, info.Timeout); err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *Remote) ChangeAccess(info AccessChangeInfo) error {
	if info.Username == "root" {
		info.OldPermission = "%"
		info.Name = "*"
	}
	if info.Permission != info.OldPermission {
		if err := r.Delete(DeleteInfo{
			Version:     info.Version,
			Username:    info.Username,
			Permission:  info.OldPermission,
			ForceDelete: true,
			Timeout:     300}); err != nil {
			return err
		}
		if info.Username == "root" {
			return nil
		}
	}
	if err := r.CreateUser(CreateInfo{
		Name:       info.Name,
		Version:    info.Version,
		Username:   info.Username,
		Password:   info.Password,
		Permission: info.Permission,
		Timeout:    info.Timeout,
	}); err != nil {
		return err
	}
	if err := r.ExecSQL("flush privileges", 300); err != nil {
		return err
	}
	return nil
}

func (r *Remote) Backup(info BackupInfo) (string, error) {
	fileOp := files.NewFileOp()
	if !fileOp.Stat(info.TargetDir) {
		if err := os.MkdirAll(info.TargetDir, os.ModePerm); err != nil {
			return "", fmt.Errorf("mkdir %s failed, err: %v", info.TargetDir, err)
		}
	}
	fileName := fmt.Sprintf("%s/%s_%s.sql", info.TargetDir, info.Name, time.Now().Format("20060102150405"))
	dns := fmt.Sprintf("%s:%s@tcp(%s:%v)/%s?charset=%s&parseTime=true&loc=Asia%sShanghai", r.User, r.Password, r.Address, r.Port, info.Name, info.Format, "%2F")

	f, _ := os.OpenFile(fileName, os.O_RDWR|os.O_CREATE, 0755)
	defer f.Close()
	if err := mysqldump.Dump(dns, mysqldump.WithData(), mysqldump.WithWriter(f)); err != nil {
		return "", err
	}

	if err := fileOp.Compress([]string{fileName}, info.TargetDir, path.Base(fileName)+".gz", files.Gz); err != nil {
		return "", err
	}
	return fileName, nil
}

func (r *Remote) Recover(info RecoverInfo) error {
	fileOp := files.NewFileOp()
	fileName := info.SourceFile
	if strings.HasSuffix(info.SourceFile, ".sql.gz") {
		fileName = strings.TrimSuffix(info.SourceFile, ".gz")
		if err := fileOp.Decompress(info.SourceFile, fileName, files.Gz); err != nil {
			return err
		}
	}
	if strings.HasSuffix(info.SourceFile, ".tar.gz") {
		fileName = strings.TrimSuffix(info.SourceFile, ".tar.gz")
		if err := fileOp.Decompress(info.SourceFile, fileName, files.TarGz); err != nil {
			return err
		}
	}
	dns := fmt.Sprintf("%s:%s@tcp(%s:%v)/%s?charset=%s&parseTime=true&loc=Asia%sShanghai", r.User, r.Password, r.Address, r.Port, info.Name, info.Format, "%2F")
	f, err := os.Open(fileName)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := mysqldump.Source(dns, f); err != nil {
		return err
	}
	return nil
}

func (r *Remote) Close() {
	_ = r.Client.Close()
}

func (r *Remote) ExecSQL(command string, timeout uint) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	if _, err := r.Client.ExecContext(ctx, command); err != nil {
		return err
	}
	if ctx.Err() == context.DeadlineExceeded {
		return buserr.New(constant.ErrExecTimeOut)
	}

	return nil
}

func (r *Remote) ExecSQLForHosts(timeout uint) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	results, err := r.Client.QueryContext(ctx, "select host from mysql.user where user='root';")
	if err != nil {
		return nil, err
	}
	if ctx.Err() == context.DeadlineExceeded {
		return nil, buserr.New(constant.ErrExecTimeOut)
	}
	var rows []string
	for results.Next() {
		var host string
		if err := results.Scan(&host); err != nil {
			continue
		}
		rows = append(rows, host)
	}
	return rows, nil
}
